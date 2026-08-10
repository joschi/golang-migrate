// Package testing has the database tests.
// All database drivers must pass the Test function.
// This lives in it's own package so it stays a test dependency.
package testing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
)

// Test runs tests against database implementations.
func Test(t *testing.T, d database.Driver, migration []byte) {
	if migration == nil {
		t.Fatal("test must provide migration reader")
	}

	TestNilVersion(t, d) // test first
	TestLockAndUnlock(t, d)
	TestRun(t, d, bytes.NewReader(migration))
	TestSetVersion(t, d) // also tests Version()
	// Drop breaks the driver, so test it last.
	TestDrop(t, d)
}

func TestNilVersion(t *testing.T, d database.Driver) {
	v, _, err := d.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != database.NilVersion {
		t.Fatalf("Version: expected version to be NilVersion (-1), got %v", v)
	}
}

const (
	// lockTestTimeout bounds the lock sequence. It is passed to the driver as a
	// context deadline, so a driver that honors cancellation — as
	// database.Driver requires — ends the test itself rather than being
	// abandoned. Lock is the only method with a waiting contract, which is why
	// the other helpers here use an unbounded context.
	lockTestTimeout = 15 * time.Second

	// lockTestWatchdog is how long to wait past that deadline before declaring
	// the driver stuck. The slack absorbs a driver that honors ctx but needs a
	// moment to unwind an in-flight query.
	lockTestWatchdog = lockTestTimeout + 5*time.Second
)

func TestLockAndUnlock(t *testing.T, d database.Driver) {
	ctx, cancel := context.WithTimeout(context.Background(), lockTestTimeout)
	defer cancel()

	// The sequence runs in a goroutine only so that a driver which ignores ctx
	// altogether fails this one test instead of wedging the whole test binary.
	// A driver that honors ctx unblocks on the deadline above and reports here.
	errs := make(chan error, 1)
	go func() { errs <- lockSequence(ctx, d) }()

	select {
	case err := <-errs:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(lockTestWatchdog):
		t.Fatalf("Lock/Unlock still running %v after its %v deadline, looks like a deadlock"+
			" (driver ignoring ctx?)\n%#v", lockTestWatchdog, lockTestTimeout, d)
	}
}

// lockSequence exercises the Driver locking contract and returns the first
// violation it finds.
func lockSequence(ctx context.Context, d database.Driver) error {
	// Unlocking before locking must report ErrNotLocked, which also confirms the
	// driver starts out unlocked.
	if err := d.Unlock(ctx); !errors.Is(err, database.ErrNotLocked) {
		return fmt.Errorf("unlock before lock: expected an error matching database.ErrNotLocked, got %v", err)
	}

	if err := d.Lock(ctx); err != nil {
		return fmt.Errorf("lock: %w", err)
	}

	// Acquiring an already held lock must report ErrLocked, so that callers and
	// instrumentation can tell contention from an unrelated failure.
	if err := d.Lock(ctx); !errors.Is(err, database.ErrLocked) {
		return fmt.Errorf("lock while locked: expected an error matching database.ErrLocked, got %v", err)
	}

	if err := d.Unlock(ctx); err != nil {
		return fmt.Errorf("unlock: %w", err)
	}

	// The driver must be reusable after unlocking.
	if err := d.Lock(ctx); err != nil {
		return fmt.Errorf("re-lock: %w", err)
	}
	if err := d.Unlock(ctx); err != nil {
		return fmt.Errorf("re-unlock: %w", err)
	}
	return nil
}

func TestRun(t *testing.T, d database.Driver, migration io.Reader) {
	if migration == nil {
		t.Fatal("migration can't be nil")
	}

	if err := d.Run(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
}

func TestDrop(t *testing.T, d database.Driver) {
	if err := d.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSetVersion(t *testing.T, d database.Driver) {
	testCases := []struct {
		name            string
		version         int
		dirty           bool
		expectedErr     error
		expectedReadErr error
		expectedVersion int
		expectedDirty   bool
	}{
		{name: "set 1 dirty", version: 1, dirty: true, expectedErr: nil, expectedReadErr: nil, expectedVersion: 1, expectedDirty: true},
		{name: "re-set 1 dirty", version: 1, dirty: true, expectedErr: nil, expectedReadErr: nil, expectedVersion: 1, expectedDirty: true},
		{name: "set 2 clean", version: 2, dirty: false, expectedErr: nil, expectedReadErr: nil, expectedVersion: 2, expectedDirty: false},
		{name: "re-set 2 clean", version: 2, dirty: false, expectedErr: nil, expectedReadErr: nil, expectedVersion: 2, expectedDirty: false},
		{name: "last migration dirty", version: database.NilVersion, dirty: true, expectedErr: nil, expectedReadErr: nil, expectedVersion: database.NilVersion, expectedDirty: true},
		{name: "last migration clean", version: database.NilVersion, dirty: false, expectedErr: nil, expectedReadErr: nil, expectedVersion: database.NilVersion, expectedDirty: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := d.SetVersion(context.Background(), tc.version, tc.dirty)
			if !errors.Is(err, tc.expectedErr) {
				t.Fatal("Got unexpected error:", err, "!=", tc.expectedErr)
			}
			v, dirty, readErr := d.Version(context.Background())
			if !errors.Is(readErr, tc.expectedReadErr) {
				t.Fatal("Got unexpected error:", readErr, "!=", tc.expectedReadErr)
			}
			if v != tc.expectedVersion {
				t.Error("Got unexpected version:", v, "!=", tc.expectedVersion)
			}
			if dirty != tc.expectedDirty {
				t.Error("Got unexpected dirty value:", dirty, "!=", tc.dirty)
			}
		})
	}
}
