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
	"fmt"
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
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
	// ports. The mapped ports are then accessible via ContainerInfo.
	PortRequired bool
	// ExposedPorts explicitly exposes the given container ports (e.g.
	// "8091/tcp"), each mapped to a random host port. Use this when the image
	// does not declare the required ports via EXPOSE.
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
	if len(opts.ExposedPorts) > 0 {
		customizers = append(customizers, tc.WithExposedPorts(opts.ExposedPorts...))
	}
	if opts.PortRequired {
		customizers = append(customizers, tc.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.PublishAllPorts = true
		}))
	}
	if opts.LogStderr {
		customizers = append(customizers, tc.WithLogConsumers(&tc.StdoutLogConsumer{}))
	}

	startCtx, cancel := context.WithTimeout(ctx, opts.PullTimeout+opts.Timeout)
	defer cancel()

	ctr, err := tc.Run(startCtx, spec.ImageName, customizers...)
	tc.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("Failed to start container for image %s: %v", spec.ImageName, err)
	}

	info, err := newContainerInfo(ctx, ctr)
	if err != nil {
		t.Fatalf("Failed to inspect container for image %s: %v", spec.ImageName, err)
	}

	if !waitReady(ctx, info, opts) {
		t.Fatalf("Timed out waiting for container to get ready: %s", spec.ImageName)
	}

	testFunc(t, info)
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

func waitReady(ctx context.Context, info ContainerInfo, opts Options) bool {
	if opts.ReadyFunc == nil {
		return true
	}

	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	check := func() bool {
		readyCtx, readyCancel := context.WithTimeout(runCtx, opts.ReadyTimeout)
		defer readyCancel()
		return opts.ReadyFunc(readyCtx, info)
	}

	// Check immediately so a container that is already up is not delayed by a
	// full tick.
	if check() {
		return true
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if check() {
				return true
			}
		case <-runCtx.Done():
			return false
		}
	}
}
