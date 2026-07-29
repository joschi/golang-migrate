// Package dktesting provides helpers for running golang-migrate's database
// driver tests against throwaway Docker containers using testcontainers-go.
//
// ParallelTest runs a test function against a matrix of image versions, each
// as a parallel subtest: it starts a container per version, waits on a
// driver-supplied readiness check, and hands the test a ContainerInfo exposing
// the container's published ports and an exec helper.
package dktesting

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/exec"
)

// Default timeouts applied when the corresponding Options field is left zero.
const (
	// DefaultPullTimeout is the default timeout used when pulling images and
	// starting a container.
	DefaultPullTimeout = time.Minute
	// DefaultTimeout is the default timeout used while waiting for a container
	// to become ready.
	DefaultTimeout = time.Minute
	// DefaultReadyTimeout is the default timeout used for each container ready
	// check.
	DefaultReadyTimeout = 2 * time.Second
)

// Options holds the configuration for a test container.
type Options struct {
	// Env sets environment variables inside the container.
	Env map[string]string
	// Cmd overrides the container command (and arguments).
	Cmd []string
	// PortRequired publishes all ports exposed by the image to random host
	// ports, which are then accessible via ContainerInfo.Port.
	//
	// This is already what testcontainers does whenever ExposedPorts is empty,
	// so the field needs no handling of its own; it is retained because the
	// driver tests set it and it documents their intent. Do not additionally
	// set HostConfig.PublishAllPorts to implement it: that leaves Docker with
	// two overlapping instructions to publish the same ports, which some
	// daemons reject with "address already in use".
	PortRequired bool
	// ExposedPorts publishes only the given container ports (e.g. "8091/tcp"),
	// each mapped to a random host port. Set this to avoid publishing every
	// port an image exposes when only a few are needed.
	ExposedPorts []string
	// ReadyFunc is polled (once per second) until it returns true or Timeout
	// elapses. Each invocation receives a context bounded by ReadyTimeout.
	ReadyFunc func(context.Context, ContainerInfo) bool
	// PullTimeout bounds image pull and container start.
	PullTimeout time.Duration
	// Timeout bounds the overall wait for the container to become ready.
	Timeout time.Duration
	// ReadyTimeout bounds each individual ReadyFunc invocation.
	ReadyTimeout time.Duration
	// LogStderr streams the container logs to stdout, which is useful for
	// debugging failing containers.
	LogStderr bool
}

func (o *Options) init() {
	if o.PullTimeout <= 0 {
		o.PullTimeout = DefaultPullTimeout
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = DefaultReadyTimeout
	}
}

// ContainerInfo holds information about a running test container.
type ContainerInfo struct {
	container tc.Container
	ports     network.PortMap
}

// Exec runs a command inside the container and returns the exit code together
// with the combined (de-multiplexed) stdout/stderr output.
func (c ContainerInfo) Exec(ctx context.Context, cmd []string) (int, string, error) {
	code, reader, err := c.container.Exec(ctx, cmd, exec.Multiplexed())
	if err != nil {
		return code, "", err
	}
	out, err := io.ReadAll(reader)
	return code, string(out), err
}

func mapHost(addr netip.Addr) string {
	if !addr.IsValid() || addr.IsUnspecified() {
		return "127.0.0.1"
	}
	return addr.String()
}

// Port returns the host IP and host port that the given container port is
// published to.
func (c ContainerInfo) Port(containerPort uint16) (ip, port string, err error) {
	for p, bindings := range c.ports {
		if p.Proto() != network.TCP || p.Num() != containerPort {
			continue
		}
		for _, b := range bindings {
			return mapHost(b.HostIP), b.HostPort, nil
		}
	}
	return "", "", fmt.Errorf("container port %d is not published", containerPort)
}

// ContainerSpec holds Docker testing setup specifications.
type ContainerSpec struct {
	ImageName string
	Options   Options
}

// ParallelTest runs Docker tests in parallel.
func ParallelTest(t *testing.T, specs []ContainerSpec,
	testFunc func(*testing.T, ContainerInfo)) {

	for i, spec := range specs {
		// Only test against one version in short mode
		if i > 0 && testing.Short() {
			t.Logf("Skipping %v in short mode", spec.ImageName)
			continue
		}

		t.Run(spec.ImageName, func(t *testing.T) {
			t.Parallel()
			runContainer(t, spec, testFunc)
		})
	}
}

func runContainer(t *testing.T, spec ContainerSpec,
	testFunc func(*testing.T, ContainerInfo)) {

	opts := spec.Options
	opts.init()

	ctx := context.Background()

	customizers := []tc.ContainerCustomizer{}
	if len(opts.Env) > 0 {
		customizers = append(customizers, tc.WithEnv(opts.Env))
	}
	if len(opts.Cmd) > 0 {
		customizers = append(customizers, tc.WithCmd(opts.Cmd...))
	}
	// opts.PortRequired needs no customizer: leaving ExposedPorts unset already
	// makes testcontainers publish every port the image exposes. See the field
	// documentation on Options.
	if len(opts.ExposedPorts) > 0 {
		customizers = append(customizers, tc.WithExposedPorts(opts.ExposedPorts...))
	}
	if opts.LogStderr {
		customizers = append(customizers, tc.WithLogConsumers(&tc.StdoutLogConsumer{}))
	}

	startCtx, cancel := context.WithTimeout(ctx, opts.PullTimeout+opts.Timeout)
	defer cancel()

	ctr, err := startContainer(t, startCtx, spec.ImageName, customizers)
	if err != nil {
		t.Fatalf("Failed to start container for image %s: %v", spec.ImageName, err)
	}

	info, err := newContainerInfo(ctx, ctr)
	if err != nil {
		t.Fatalf("Failed to inspect container for image %s: %v", spec.ImageName, err)
	}

	if err := waitReady(ctx, info, opts); err != nil {
		t.Fatalf("Container for image %s never became ready: %v", spec.ImageName, err)
	}

	testFunc(t, info)
}

// Bounds on retrying a container start that failed for a transient reason.
const (
	// maxStartAttempts is the total number of times a container start is tried.
	maxStartAttempts = 3
	// startRetryDelay is how long to wait between start attempts.
	startRetryDelay = 2 * time.Second
)

// startContainer starts the image, retrying transient failures up to
// maxStartAttempts times. Every attempt's container is registered for cleanup,
// and a failed attempt is torn down immediately so it does not hold onto
// published host ports while the next attempt runs.
func startContainer(t *testing.T, ctx context.Context, image string,
	customizers []tc.ContainerCustomizer) (tc.Container, error) {

	var ctr tc.Container
	var err error

	for attempt := 1; attempt <= maxStartAttempts; attempt++ {
		ctr, err = tc.Run(ctx, image, customizers...)
		tc.CleanupContainer(t, ctr)
		if err == nil || !transientStartErr(err) {
			return ctr, err
		}

		if termErr := tc.TerminateContainer(ctr); termErr != nil {
			t.Logf("Failed to terminate container for image %s after a failed "+
				"start attempt: %v", image, termErr)
		}

		if attempt == maxStartAttempts {
			break
		}
		t.Logf("Attempt %d of %d to start container for image %s failed with a "+
			"transient error, retrying in %s: %v",
			attempt, maxStartAttempts, image, startRetryDelay, err)

		select {
		case <-time.After(startRetryDelay):
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (last start error: %v)", ctx.Err(), err)
		}
	}

	return nil, fmt.Errorf("gave up after %d attempts: %w", maxStartAttempts, err)
}

// transientStartErr reports whether err is a container start failure worth
// retrying. Docker allocates published host ports from the same ephemeral range
// the kernel hands out to outbound sockets, so binding one occasionally races
// with an unrelated process and fails even though a retry would succeed.
func transientStartErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, transient := range []string{
		"address already in use",
		"port is already allocated",
	} {
		if strings.Contains(msg, transient) {
			return true
		}
	}
	return false
}

func newContainerInfo(ctx context.Context, ctr tc.Container) (ContainerInfo, error) {
	insp, err := ctr.Inspect(ctx)
	if err != nil {
		return ContainerInfo{}, err
	}
	return ContainerInfo{
		container: ctr,
		ports:     insp.NetworkSettings.Ports,
	}, nil
}

// waitReady polls opts.ReadyFunc once per second until it reports the container
// is ready. It returns an error if the container exits or opts.Timeout elapses
// before that happens.
func waitReady(ctx context.Context, info ContainerInfo, opts Options) error {
	if opts.ReadyFunc == nil {
		return nil
	}

	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	check := func() bool {
		readyCtx, readyCancel := context.WithTimeout(runCtx, opts.ReadyTimeout)
		defer readyCancel()
		return opts.ReadyFunc(readyCtx, info)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		// Checked before the first tick so a container that is already up is
		// not delayed by a full tick.
		if check() {
			return nil
		}

		// A container that has exited will never become ready, so report why
		// instead of polling a dead container until the timeout.
		if err := info.exitErr(ctx); err != nil {
			return err
		}

		select {
		case <-ticker.C:
		case <-runCtx.Done():
			return fmt.Errorf("timed out after %s", opts.Timeout)
		}
	}
}

// exitErr describes why the container is no longer running, including its exit
// code and logs. It returns nil while the container is still running, and also
// when its state cannot be determined, so that transient inspect failures let
// the caller keep polling.
func (c ContainerInfo) exitErr(ctx context.Context) error {
	insp, err := c.container.Inspect(ctx)
	if err != nil || insp.State == nil || insp.State.Running {
		return nil
	}

	msg := fmt.Sprintf("container exited with code %d", insp.State.ExitCode)
	if insp.State.OOMKilled {
		msg += " (out of memory)"
	}
	if insp.State.Error != "" {
		msg += ": " + insp.State.Error
	}
	if logs := c.logTail(ctx, 20); logs != "" {
		msg += "\ncontainer logs:\n" + logs
	}
	return errors.New(msg)
}

// logTail returns at most the last n lines of the container's combined
// stdout/stderr, or an empty string if the logs cannot be read.
func (c ContainerInfo) logTail(ctx context.Context, n int) string {
	rc, err := c.container.Logs(ctx)
	if err != nil {
		return ""
	}
	defer func() {
		_ = rc.Close()
	}()

	out, err := io.ReadAll(rc)
	if err != nil && len(out) == 0 {
		return ""
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
