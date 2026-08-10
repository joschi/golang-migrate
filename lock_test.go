package migrate

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"

	"github.com/golang-migrate/migrate/v4/database"
	dStub "github.com/golang-migrate/migrate/v4/database/stub"
	sStub "github.com/golang-migrate/migrate/v4/source/stub"
)

// testLockTimeout only has to be long enough for the deadline to be the reason
// the call ends; the blocking driver below waits unconditionally on ctx.Done(),
// so a shorter value changes nothing but the wall clock.
const testLockTimeout = 20 * time.Millisecond

// promptly is a generous ceiling for "this call did not hang". It is deliberately
// not a multiple of testLockTimeout, so the timeout can stay small without
// making the ceiling tight on a loaded machine.
const promptly = 10 * time.Second

// openStubDriver returns a fresh in-memory database driver.
func openStubDriver(t *testing.T) database.Driver {
	t.Helper()
	d, err := (&dStub.Stub{}).Open(context.Background(), "stub://")
	require.NoError(t, err)
	return d
}

// blockingDriver blocks in Lock and Unlock until its context is done, the way
// postgres blocks in pg_advisory_lock and sqlserver in sp_getapplock. It records
// the context error it observed, so a test can prove the attempt was canceled
// rather than abandoned.
type blockingDriver struct {
	database.Driver

	entered   chan struct{}
	sawCancel chan error
}

func newBlockingDriver(t *testing.T) *blockingDriver {
	t.Helper()
	return &blockingDriver{
		Driver:    openStubDriver(t),
		entered:   make(chan struct{}),
		sawCancel: make(chan error, 2),
	}
}

func (d *blockingDriver) block(ctx context.Context) error {
	<-ctx.Done()
	d.sawCancel <- ctx.Err()
	return ctx.Err()
}

func (d *blockingDriver) Lock(ctx context.Context) error {
	close(d.entered)
	return d.block(ctx)
}

func (d *blockingDriver) Unlock(ctx context.Context) error { return d.block(ctx) }

// requireSawCancel asserts the driver observed its context ending with want,
// which is what distinguishes a canceled call from an abandoned one.
func requireSawCancel(t *testing.T, drv *blockingDriver, want error) {
	t.Helper()
	select {
	case ctxErr := <-drv.sawCancel:
		assert.ErrorIs(t, ctxErr, want)
	case <-time.After(promptly):
		t.Fatal("driver call was abandoned, not canceled: it never observed ctx.Done()")
	}
}

// ctxAwareDriver honors context cancellation the way a real SQL driver does: a
// done context fails the call rather than being ignored. The stub driver is a
// pure in-process CAS that never looks at ctx, so it cannot show whether a
// context was usable.
type ctxAwareDriver struct {
	database.Driver
}

func (d *ctxAwareDriver) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.Driver.Lock(ctx)
}

func (d *ctxAwareDriver) Unlock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.Driver.Unlock(ctx)
}

// newMigrateWithDriver builds a Migrate over the stub source and the given
// database driver, with telemetry sent to the returned exporter and reader.
func newMigrateWithDriver(t *testing.T, drv database.Driver) (*Migrate, *tracetest.InMemoryExporter, sdkmetric.Reader) {
	t.Helper()

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	ctx := context.Background()
	src, err := (&sStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)
	src.(*sStub.Stub).Migrations = sourceStubMigrations

	m, err := NewWithInstance(ctx, "stub", src, "stub", drv,
		WithTracerProvider(tp), WithMeterProvider(mp))
	require.NoError(t, err)
	m.LockTimeout = testLockTimeout
	return m, exp, reader
}

// TestLockTimeoutReturnsErrLockTimeout covers the basic contract: a driver that
// cannot acquire the lock in time yields ErrLockTimeout, promptly.
func TestLockTimeoutReturnsErrLockTimeout(t *testing.T) {
	drv := newBlockingDriver(t)
	m, _, _ := newMigrateWithDriver(t, drv)

	start := time.Now()
	err := m.lock(context.Background())
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLockTimeout)
	assert.Less(t, elapsed, promptly, "lock did not return promptly")
	assert.False(t, m.isLocked, "isLocked must stay false when acquisition failed")
}

// TestLockTimeoutCancelsTheDriver is the heart of the fix. Before it, the
// timeout abandoned the in-flight Lock call: the driver kept blocking on an
// uncanceled context and could acquire the lock after migrate gave up,
// orphaning a server-side lock that nothing would release.
func TestLockTimeoutCancelsTheDriver(t *testing.T) {
	drv := newBlockingDriver(t)
	m, _, _ := newMigrateWithDriver(t, drv)

	require.ErrorIs(t, m.lock(context.Background()), ErrLockTimeout)

	requireSawCancel(t, drv, context.DeadlineExceeded)
}

// TestLockTimeoutLeavesNoGoroutine guards against the leaked goroutine the old
// implementation left behind, which held a dedicated database connection for
// the rest of the process.
func TestLockTimeoutLeavesNoGoroutine(t *testing.T) {
	drv := newBlockingDriver(t)
	m, _, _ := newMigrateWithDriver(t, drv)

	before := runtime.NumGoroutine()
	require.ErrorIs(t, m.lock(context.Background()), ErrLockTimeout)

	// lock is synchronous now, so there is nothing to wait for: any goroutine it
	// spawned would still be running here.
	after := runtime.NumGoroutine()
	assert.LessOrEqual(t, after, before,
		"lock leaked a goroutine (before=%d after=%d)", before, after)
}

// TestLockSpanEndsWithinParent is the regression test for the trace defect: the
// lock span is a child of migrate.up, so it must not outlive it. The abandoned
// goroutine used to end the span hundreds of milliseconds after its parent.
func TestLockSpanEndsWithinParent(t *testing.T) {
	drv := newBlockingDriver(t)
	m, exp, _ := newMigrateWithDriver(t, drv)

	require.ErrorIs(t, m.Up(context.Background()), ErrLockTimeout)

	// Both spans are already exported: lock returns only after the driver call
	// unwinds, and OTelDriver.Lock ends its span before returning.
	var parent, child sdktrace.ReadOnlySpan
	for _, s := range exp.GetSpans().Snapshots() {
		switch s.Name() {
		case spanNameUp:
			parent = s
		case "lock":
			child = s
		}
	}
	require.NotNil(t, parent, "migrate.up span missing")
	require.NotNil(t, child, "lock span missing")

	assert.Equal(t, parent.SpanContext().SpanID(), child.Parent().SpanID(),
		"lock must be a child of migrate.up")
	assert.False(t, child.EndTime().After(parent.EndTime()),
		"lock span ended %s after its parent migrate.up",
		child.EndTime().Sub(parent.EndTime()))
}

// TestLockCallerCancellationIsNotATimeout checks the classification: the
// caller's context ending is not a lock timeout and must not be reported as one.
func TestLockCallerCancellationIsNotATimeout(t *testing.T) {
	drv := newBlockingDriver(t)
	m, _, _ := newMigrateWithDriver(t, drv)
	m.LockTimeout = time.Hour // ensure our own deadline cannot fire

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-drv.entered
		cancel()
	}()

	err := m.lock(ctx)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrLockTimeout,
		"a canceled caller context must not be reported as a lock timeout")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestLockTimeoutMetrics checks the instruments still fire on the timeout path.
func TestLockTimeoutMetrics(t *testing.T) {
	drv := newBlockingDriver(t)
	m, _, reader := newMigrateWithDriver(t, drv)

	ctx := context.Background()
	require.ErrorIs(t, m.lock(ctx), ErrLockTimeout)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	assert.True(t, hasHistogramData(rm, "migrate.lock.duration"),
		"migrate.lock.duration must be recorded on the timeout path")

	count, ok := findCounter(rm, "migrate.lock.failures")
	require.True(t, ok, "migrate.lock.failures must be recorded")
	assert.Equal(t, int64(1), count)
	assert.True(t, counterHasAttr(rm, "migrate.lock.failures", semconv.ErrorTypeKey, "timeout"),
		"migrate.lock.failures must carry error.type=timeout")
}

// TestUnlockIsBounded covers the companion fix: unlock had no timeout at all,
// so a driver that never returned would hang the migration forever.
func TestUnlockIsBounded(t *testing.T) {
	drv := newBlockingDriver(t)
	m, _, _ := newMigrateWithDriver(t, drv)

	start := time.Now()
	err := m.unlock(context.Background())

	require.Error(t, err, "a blocking Unlock must not report success")
	assert.Less(t, time.Since(start), promptly, "unlock did not return promptly")

	requireSawCancel(t, drv, context.DeadlineExceeded)
}

// TestUnlockSurvivesCallerCancellation covers the reason unlock detaches from
// the caller's context. Releasing the lock is cleanup, and it normally runs while
// handling a failure the caller's cancellation caused; deriving the deadline from
// that context makes it born expired, so the lock would never be released.
func TestUnlockSurvivesCallerCancellation(t *testing.T) {
	m, _, _ := newMigrateWithDriver(t, &ctxAwareDriver{Driver: openStubDriver(t)})

	require.NoError(t, m.lock(context.Background()))
	require.True(t, m.isLocked)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, m.unlock(ctx), "unlock must still release with a canceled caller")
	assert.False(t, m.isLocked, "isLocked must be cleared after a successful unlock")
}

// TestLockAbortsOnGracefulStop covers a stop requested while still waiting for
// the lock. It used to be ignored, so the caller waited out the whole
// LockTimeout; the attempt is now canceled promptly.
func TestLockAbortsOnGracefulStop(t *testing.T) {
	drv := newBlockingDriver(t)
	m, _, _ := newMigrateWithDriver(t, drv)
	m.LockTimeout = time.Hour // only a stop can end this wait

	go func() {
		<-drv.entered
		m.GracefulStop <- true
	}()

	start := time.Now()
	err := m.lock(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, errGracefulStop)
	assert.NotErrorIs(t, err, ErrLockTimeout, "a stop is not a timeout")
	assert.Less(t, time.Since(start), promptly, "lock did not abort promptly")
	assert.False(t, m.isLocked)

	requireSawCancel(t, drv, context.Canceled)

	// The signal must still be visible to the migration loop, even though lock
	// drained the channel rather than stop().
	assert.True(t, m.stop(), "graceful stop must stay latched after lock consumed it")
}
