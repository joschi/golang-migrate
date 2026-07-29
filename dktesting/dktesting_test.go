package dktesting

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
)

// startForTest starts the container described by spec and returns a
// ContainerInfo for it. It does not wait for readiness.
func startForTest(t *testing.T, spec ContainerSpec) ContainerInfo {
	t.Helper()

	ctx := context.Background()

	customizers := []tc.ContainerCustomizer{}
	if len(spec.Options.Env) > 0 {
		customizers = append(customizers, tc.WithEnv(spec.Options.Env))
	}
	if len(spec.Options.Cmd) > 0 {
		customizers = append(customizers, tc.WithCmd(spec.Options.Cmd...))
	}

	ctr, err := tc.Run(ctx, spec.ImageName, customizers...)
	tc.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("failed to start container for image %s: %v", spec.ImageName, err)
	}

	info, err := newContainerInfo(ctx, ctr)
	if err != nil {
		t.Fatalf("failed to inspect container for image %s: %v", spec.ImageName, err)
	}
	return info
}

// TestWaitReadyContainerExits verifies that a container which exits before
// becoming ready fails immediately with its exit code and logs, rather than
// polling a dead container until Timeout elapses.
func TestWaitReadyContainerExits(t *testing.T) {
	const (
		exitCode = 3
		marker   = "simulated startup failure"
		timeout  = 30 * time.Second
	)

	spec := ContainerSpec{
		ImageName: "alpine:3",
		Options: Options{
			Cmd: []string{"sh", "-c",
				"echo '" + marker + "' >&2; sleep 1; exit " + strconv.Itoa(exitCode)},
			// Never ready, so only the container exiting can end the wait.
			ReadyFunc: func(context.Context, ContainerInfo) bool { return false },
			Timeout:   timeout,
		},
	}

	info := startForTest(t, spec)

	start := time.Now()
	err := waitReady(context.Background(), info, spec.Options)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("waitReady returned nil for a container that exited")
	}
	if got := err.Error(); !strings.Contains(got, "exited with code 3") {
		t.Errorf("error should report the exit code, got: %s", got)
	}
	if got := err.Error(); !strings.Contains(got, marker) {
		t.Errorf("error should include container logs containing %q, got: %s", marker, got)
	}
	// Fail-fast: must not have waited out the full timeout.
	if elapsed >= timeout {
		t.Errorf("waitReady took %s, expected it to fail fast well before %s", elapsed, timeout)
	}
}

// TestWaitReadyTimeout verifies that a container which stays running but never
// reports ready still fails with a timeout error.
func TestWaitReadyTimeout(t *testing.T) {
	const timeout = 3 * time.Second

	spec := ContainerSpec{
		ImageName: "alpine:3",
		Options: Options{
			Cmd:       []string{"sleep", "300"},
			ReadyFunc: func(context.Context, ContainerInfo) bool { return false },
			Timeout:   timeout,
		},
	}

	info := startForTest(t, spec)

	err := waitReady(context.Background(), info, spec.Options)
	if err == nil {
		t.Fatal("waitReady returned nil for a container that never became ready")
	}
	if got := err.Error(); !strings.Contains(got, "timed out") {
		t.Errorf("error should report a timeout, got: %s", got)
	}
}

// TestWaitReadyNoReadyFunc verifies that a nil ReadyFunc is treated as ready.
func TestWaitReadyNoReadyFunc(t *testing.T) {
	if err := waitReady(context.Background(), ContainerInfo{}, Options{}); err != nil {
		t.Fatalf("waitReady with no ReadyFunc: %v", err)
	}
}

func TestTransientStartErr(t *testing.T) {
	// Verbatim from a CircleCI run, so the classifier is checked against the
	// error it actually has to recognize rather than a paraphrase of it.
	const observed = "generic container: start container: container start: " +
		"Error response from daemon: failed to set up container networking: " +
		"driver failed programming external connectivity on endpoint " +
		"goofy_elion (6870c6df50528dede84f609ad9b94c659868465672948324bd): " +
		"failed to listen on TCP socket: address already in use"

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"observed CircleCI failure", errors.New(observed), true},
		{"port already allocated", errors.New(
			"Error response from daemon: driver failed programming external " +
				"connectivity on endpoint x: Bind for 0.0.0.0:33795 failed: " +
				"port is already allocated"), true},
		{"wrapped", fmt.Errorf("start %s: %w", "img",
			errors.New("address already in use")), true},
		{"image not found", errors.New(
			"Error response from daemon: pull access denied for nope"), false},
		{"unrelated", errors.New("context deadline exceeded"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transientStartErr(tt.err); got != tt.want {
				t.Errorf("transientStartErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
