// Package database provides the Driver interface.
// All database drivers must implement this interface, register themselves,
// optionally provide a `WithInstance` function and pass the tests
// in package database/testing.
package database

import (
	"context"
	"fmt"
	"io"
	"sync"

	iurl "github.com/golang-migrate/migrate/v5/internal/url"
)

var (
	ErrLocked    = fmt.Errorf("can't acquire lock")
	ErrNotLocked = fmt.Errorf("can't unlock, as not currently locked")
)

const NilVersion int = -1

var driversMu sync.RWMutex
var drivers = make(map[string]Driver)

// Driver is the interface every database driver must implement.
//
// How to implement a database driver?
//  1. Implement this interface.
//  2. Optionally, add a function named `WithInstance`.
//     This function should accept an existing DB instance and a Config{} struct
//     and return a driver instance.
//  3. Add a test that calls database/testing.go:Test()
//  4. Add own tests for Open(), WithInstance() (when provided) and Close().
//     All other functions are tested by tests in database/testing.
//     Saves you some time and makes sure all database drivers behave the same way.
//  5. Call Register in init().
//  6. Create a cmd/migrate/internal/cli/build_<driver-name>.go file
//  7. Add driver name in 'DATABASE' variable in Makefile
//
// Guidelines:
//   - Don't try to correct user input. Don't assume things.
//     When in doubt, return an error and explain the situation to the user.
//   - All configuration input must come from the URL string in func Open()
//     or the Config{} struct in WithInstance. Don't os.Getenv().
//   - Honor context cancellation. Pass ctx through to the calls you make
//     (ExecContext, QueryRowContext, and equivalents) and return promptly once
//     it is done; a method that blocks while ignoring ctx stalls the migration
//     instead of timing out.
type Driver interface {
	// Open returns a new driver instance configured with parameters
	// coming from the URL string. Migrate will call this function
	// only once per instance.
	Open(ctx context.Context, url string) (Driver, error)

	// Close closes the underlying database instance managed by the driver.
	// Migrate will call this function only once per instance.
	Close(ctx context.Context) error

	// Lock should acquire a database lock so that only one migration process
	// can run at a time. Migrate will call this function before Run is called.
	// If the implementation can't provide this functionality, return nil.
	// Return database.ErrLocked if database is already locked.
	//
	// ctx carries Migrate's lock timeout as a deadline. Implementations that
	// wait for the lock must abort and return when ctx is done, so that a
	// contended lock times out instead of blocking the migration. Returning the
	// context error is fine; Migrate translates its own deadline into
	// ErrLockTimeout. A driver with its own wait budget (mysql's GET_LOCK, for
	// example) may give up sooner, in which case that budget wins.
	Lock(ctx context.Context) error

	// Unlock should release the lock. Migrate will call this function after
	// all migrations have been run. As with Lock, ctx carries a deadline and
	// implementations must return when it is done.
	Unlock(ctx context.Context) error

	// Run applies a migration to the database. migration is guaranteed to be not nil.
	Run(ctx context.Context, migration io.Reader) error

	// SetVersion saves version and dirty state.
	// Migrate will call this function before and after each call to Run.
	// version must be >= -1. -1 means NilVersion.
	SetVersion(ctx context.Context, version int, dirty bool) error

	// Version returns the currently active version and if the database is dirty.
	// When no migration has been applied, it must return version -1.
	// Dirty means, a previous migration failed and user interaction is required.
	Version(ctx context.Context) (version int, dirty bool, err error)

	// Drop deletes everything in the database.
	// Note that this is a breaking action, a new call to Open() is necessary to
	// ensure subsequent calls work as expected.
	Drop(ctx context.Context) error
}

// MigrationsTabler is an optional interface a Driver may implement to report
// the table or collection it keeps migration state in.
//
// OTelDriver uses it to populate the db.collection.name attribute and to build
// span names of the form "{db.operation.name} {target}", as the database
// semantic conventions prescribe. It is consulted only for operations that act
// on that one table (reading and writing the version); Run executes arbitrary
// migration SQL and Drop targets the whole database, so neither reports a
// collection.
//
// Drivers that do not implement it still emit spans, named after the operation
// alone.
type MigrationsTabler interface {
	// MigrationsTable returns the table or collection holding migration state,
	// or an empty string when that is unknown or not applicable.
	MigrationsTable() string
}

// migrationsTableOf returns the migrations table reported by driver, or an empty
// string if it does not report one.
func migrationsTableOf(driver Driver) string {
	if t, ok := driver.(MigrationsTabler); ok {
		return t.MigrationsTable()
	}
	return ""
}

// Open returns a new driver instance.
func Open(ctx context.Context, url string) (Driver, error) {
	scheme, err := iurl.SchemeFromURL(url)
	if err != nil {
		return nil, err
	}

	driversMu.RLock()
	d, ok := drivers[scheme]
	driversMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("database driver: unknown driver %v (forgotten import?)", scheme)
	}

	return d.Open(ctx, url)
}

// Register globally registers a driver.
func Register(name string, driver Driver) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if driver == nil {
		panic("Register driver is nil")
	}
	if _, dup := drivers[name]; dup {
		panic("Register called twice for driver " + name)
	}
	drivers[name] = driver
}

// List lists the registered drivers
func List() []string {
	driversMu.RLock()
	defer driversMu.RUnlock()
	names := make([]string, 0, len(drivers))
	for n := range drivers {
		names = append(names, n)
	}
	return names
}
