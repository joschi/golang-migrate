// Package migrate reads migrations from sources and runs them against databases.
// Sources are defined by the `source.Driver` and databases by the `database.Driver`
// interface. The driver interfaces are kept "dumb", all migration logic is kept
// in this package.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/internal/otelconv"
	iurl "github.com/golang-migrate/migrate/v4/internal/url"
	"github.com/golang-migrate/migrate/v4/source"
)

// DefaultPrefetchMigrations sets the number of migrations to pre-read
// from the source. This is helpful if the source is remote, but has little
// effect for a local source (i.e. file system).
// Please note that this setting has a major impact on the memory usage,
// since each pre-read migration is buffered in memory. See DefaultBufferSize.
var DefaultPrefetchMigrations = uint(10)

// DefaultLockTimeout sets the max time a database driver has to acquire a lock.
var DefaultLockTimeout = 15 * time.Second

var (
	ErrNoChange       = errors.New("no change")
	ErrNilVersion     = errors.New("no migration")
	ErrInvalidVersion = errors.New("version must be >= -1")
	ErrLocked         = errors.New("database locked")
	ErrLockTimeout    = errors.New("timeout: can't acquire database lock")
	// ErrNoMigrationFiles is returned when the source contains no migration
	// files, which usually means the source path is wrong or the directory is
	// empty. It wraps os.ErrNotExist for backwards compatibility.
	ErrNoMigrationFiles = fmt.Errorf("no migration files found in source: %w", os.ErrNotExist)
)

// ErrShortLimit is an error returned when not enough migrations
// can be returned by a source for a given limit.
type ErrShortLimit struct {
	Short uint
}

// Error implements the error interface.
func (e ErrShortLimit) Error() string {
	return fmt.Sprintf("limit %v short", e.Short)
}

type ErrDirty struct {
	Version int
}

func (e ErrDirty) Error() string {
	return fmt.Sprintf("Dirty database version %v. Fix and force version.", e.Version)
}

type Migrate struct {
	sourceName         string
	sourceDrv          source.Driver
	databaseDriverName string
	databaseDrv        database.Driver

	// Log accepts a Logger interface
	Log Logger

	// GracefulStop accepts `true` and will stop executing migrations
	// as soon as possible at a safe break point, so that the database
	// is not corrupted.
	GracefulStop chan bool
	isLockedMu   *sync.Mutex

	isGracefulStop bool
	isLocked       bool

	// PrefetchMigrations defaults to DefaultPrefetchMigrations,
	// but can be set per Migrate instance.
	PrefetchMigrations uint

	// LockTimeout defaults to DefaultLockTimeout,
	// but can be set per Migrate instance.
	LockTimeout time.Duration

	// otel holds the tracer, configuration and pre-created metric instruments.
	otelConfig      config
	otelTracer      trace.Tracer
	otelInstruments otelInstruments

	// otelBaseAttrs and otelMetricAttrs are derived from sourceName and
	// databaseDriverName, which never change after construction, so they are
	// built once by initOTelAttrs rather than on every span and measurement.
	otelBaseAttrs   []attribute.KeyValue
	otelMetricAttrs metric.MeasurementOption
}

// New returns a new Migrate instance from a source URL and a database URL.
// The URL scheme is defined by each driver.
func New(ctx context.Context, sourceURL, databaseURL string, opts ...Option) (retM *Migrate, retErr error) {
	m := newCommon(opts)

	ctx, span := m.startOpenSpan(ctx)
	defer func() { endSpan(span, retErr) }()

	sourceName, err := iurl.SchemeFromURL(sourceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse scheme from source URL: %w", err)
	}
	m.sourceName = sourceName

	databaseDriverName, err := iurl.SchemeFromURL(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse scheme from database URL: %w", err)
	}
	m.databaseDriverName = databaseDriverName
	m.initOTelAttrs()
	span.SetAttributes(m.otelAttrs()...)

	sourceDrv, err := source.Open(ctx, sourceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open source, %q: %w", sourceURL, err)
	}
	m.sourceDrv = source.NewOTelDriver(sourceDrv, sourceName, m.otelConfig.sourceOTelOptions()...)

	databaseDrv, err := database.Open(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", database.RedactPassword(err))
	}
	m.databaseDrv = database.NewOTelDriver(databaseDrv, databaseDriverName, m.otelConfig.databaseOTelOptions()...)

	return m, nil
}

// NewWithDatabaseInstance returns a new Migrate instance from a source URL
// and an existing database instance. The source URL scheme is defined by each driver.
// Use any string that can serve as an identifier during logging as databaseDriverName.
// You are responsible for closing the underlying database client if necessary.
func NewWithDatabaseInstance(ctx context.Context, sourceURL string, databaseDriverName string, databaseInstance database.Driver, opts ...Option) (retM *Migrate, retErr error) {
	m := newCommon(opts)

	ctx, span := m.startOpenSpan(ctx)
	defer func() { endSpan(span, retErr) }()

	sourceName, err := iurl.SchemeFromURL(sourceURL)
	if err != nil {
		return nil, err
	}
	m.sourceName = sourceName

	m.databaseDriverName = databaseDriverName
	m.initOTelAttrs()
	span.SetAttributes(m.otelAttrs()...)

	sourceDrv, err := source.Open(ctx, sourceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open source, %q: %w", sourceURL, err)
	}
	m.sourceDrv = source.NewOTelDriver(sourceDrv, sourceName, m.otelConfig.sourceOTelOptions()...)

	m.databaseDrv = database.NewOTelDriver(databaseInstance, databaseDriverName, m.otelConfig.databaseOTelOptions()...)

	return m, nil
}

// NewWithSourceInstance returns a new Migrate instance from an existing source instance
// and a database URL. The database URL scheme is defined by each driver.
// Use any string that can serve as an identifier during logging as sourceName.
// You are responsible for closing the underlying source client if necessary.
func NewWithSourceInstance(ctx context.Context, sourceName string, sourceInstance source.Driver, databaseURL string, opts ...Option) (retM *Migrate, retErr error) {
	m := newCommon(opts)

	ctx, span := m.startOpenSpan(ctx)
	defer func() { endSpan(span, retErr) }()

	databaseDriverName, err := iurl.SchemeFromURL(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse scheme from database URL: %w", err)
	}
	m.databaseDriverName = databaseDriverName

	m.sourceName = sourceName
	m.initOTelAttrs()
	span.SetAttributes(m.otelAttrs()...)

	databaseDrv, err := database.Open(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	m.databaseDrv = database.NewOTelDriver(databaseDrv, databaseDriverName, m.otelConfig.databaseOTelOptions()...)

	m.sourceDrv = source.NewOTelDriver(sourceInstance, sourceName, m.otelConfig.sourceOTelOptions()...)

	return m, nil
}

// NewWithInstance returns a new Migrate instance from an existing source and
// database instance. Use any string that can serve as an identifier during logging
// as sourceName and databaseDriverName. You are responsible for closing down
// the underlying source and database client if necessary.
func NewWithInstance(ctx context.Context, sourceName string, sourceInstance source.Driver, databaseDriverName string, databaseInstance database.Driver, opts ...Option) (*Migrate, error) {
	m := newCommon(opts)

	m.sourceName = sourceName
	m.databaseDriverName = databaseDriverName
	m.initOTelAttrs()

	m.sourceDrv = source.NewOTelDriver(sourceInstance, sourceName, m.otelConfig.sourceOTelOptions()...)
	m.databaseDrv = database.NewOTelDriver(databaseInstance, databaseDriverName, m.otelConfig.databaseOTelOptions()...)

	return m, nil
}

func newCommon(opts []Option) *Migrate {
	cfg := newConfig(opts)
	return &Migrate{
		GracefulStop:       make(chan bool, 1),
		PrefetchMigrations: DefaultPrefetchMigrations,
		LockTimeout:        DefaultLockTimeout,
		isLockedMu:         &sync.Mutex{},
		otelConfig:         cfg,
		otelTracer:         newTracer(cfg),
		otelInstruments:    newOtelInstruments(newMeter(cfg)),
	}
}

// startSpan starts an INTERNAL span for a migrate operation, carrying the
// common attributes plus any extras.
func (m *Migrate) startSpan(ctx context.Context, name string, extra ...attribute.KeyValue) (context.Context, trace.Span) {
	return m.otelTracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(m.otelAttrsWith(extra...)...),
	)
}

// startOpenSpan starts the span covering construction of a Migrate instance.
// Without it, work performed by a driver's Open (creating the migrations table,
// listing the source) would produce unparented root spans. The common
// attributes are not known yet, so callers set them once the names are parsed.
func (m *Migrate) startOpenSpan(ctx context.Context) (context.Context, trace.Span) {
	return m.otelTracer.Start(ctx, "migrate.open", trace.WithSpanKind(trace.SpanKindInternal))
}

// endSpan records err on span, unless benign, and ends it.
func endSpan(span trace.Span, err error) {
	otelSpanSetError(span, err)
	span.End()
}

// initOTelAttrs builds the cached attribute set. It must be called by every
// constructor once sourceName and databaseDriverName are known.
func (m *Migrate) initOTelAttrs() {
	m.otelBaseAttrs = []attribute.KeyValue{
		semconv.DBSystemNameKey.String(otelconv.DBSystemName(m.databaseDriverName)),
		attribute.String("migrate.source", m.sourceName),
	}
	m.otelMetricAttrs = metric.WithAttributes(m.otelBaseAttrs...)
}

// otelAttrs returns the common OTel attributes shared by all spans and metrics.
// The result is shared, so callers must not mutate or append to it; use
// otelAttrsWith to add attributes.
func (m *Migrate) otelAttrs() []attribute.KeyValue {
	return m.otelBaseAttrs
}

// otelAttrsWith returns the common attributes plus extra, in a fresh slice.
// Copying explicitly keeps callers from ever writing into the shared backing
// array, which concurrent spans would race on.
func (m *Migrate) otelAttrsWith(extra ...attribute.KeyValue) []attribute.KeyValue {
	if len(extra) == 0 {
		return m.otelBaseAttrs
	}
	attrs := make([]attribute.KeyValue, 0, len(m.otelBaseAttrs)+len(extra))
	attrs = append(attrs, m.otelBaseAttrs...)
	return append(attrs, extra...)
}

// otelSpanSetError records the error on the span unless it is a benign sentinel
// value (ErrNoChange, ErrNilVersion) that does not indicate a system failure.
// The migration body is stripped from the recorded error, so that neither the
// status description nor the exception event leaks migration SQL.
func otelSpanSetError(span trace.Span, err error) {
	if err == nil || errors.Is(err, ErrNoChange) || errors.Is(err, ErrNilVersion) {
		return
	}
	database.RecordSpanError(span, err)
}

// failureAttrs returns the metric attributes for a failed migration, adding the
// stage the failure occurred in so that a broken migration can be told apart
// from failed version bookkeeping.
func failureAttrs(base []attribute.KeyValue, stage string) metric.MeasurementOption {
	attrs := make([]attribute.KeyValue, 0, len(base)+1)
	attrs = append(attrs, base...)
	attrs = append(attrs, attribute.String("migrate.stage", stage))
	return metric.WithAttributes(attrs...)
}

// Close closes the source and the database.
func (m *Migrate) Close(ctx context.Context) (source error, database error) {
	ctx, span := m.startSpan(ctx, "migrate.close")
	defer func() { endSpan(span, errors.Join(source, database)) }()

	databaseSrvClose := make(chan error)
	sourceSrvClose := make(chan error)

	m.logVerbosePrintf("Closing source and database\n")

	go func() {
		databaseSrvClose <- m.databaseDrv.Close(ctx)
	}()

	go func() {
		sourceSrvClose <- m.sourceDrv.Close(ctx)
	}()

	return <-sourceSrvClose, <-databaseSrvClose
}

// Migrate looks at the currently active migration version,
// then migrates either up or down to the specified version.
func (m *Migrate) Migrate(ctx context.Context, version uint) (retErr error) {
	ctx, span := m.startSpan(ctx, "migrate.migrate", attribute.Int64("migrate.version", int64(version)))
	defer func() { endSpan(span, retErr) }()

	if err := m.lock(ctx); err != nil {
		return err
	}

	curVersion, dirty, err := m.databaseDrv.Version(ctx)
	if err != nil {
		return m.unlockErr(ctx, err)
	}

	if dirty {
		return m.unlockErr(ctx, ErrDirty{curVersion})
	}

	ret := make(chan interface{}, m.PrefetchMigrations)
	go m.read(ctx, curVersion, int(version), ret)

	return m.unlockErr(ctx, m.runMigrations(ctx, ret))
}

// Steps looks at the currently active migration version.
// It will migrate up if n > 0, and down if n < 0.
func (m *Migrate) Steps(ctx context.Context, n int) (retErr error) {
	if n == 0 {
		return ErrNoChange
	}

	direction := "up"
	if n < 0 {
		direction = "down"
	}
	ctx, span := m.startSpan(ctx, "migrate.steps", attribute.String("migrate.direction", direction), attribute.Int("migrate.steps", n))
	defer func() { endSpan(span, retErr) }()

	if err := m.lock(ctx); err != nil {
		return err
	}

	curVersion, dirty, err := m.databaseDrv.Version(ctx)
	if err != nil {
		return m.unlockErr(ctx, err)
	}

	if dirty {
		return m.unlockErr(ctx, ErrDirty{curVersion})
	}

	ret := make(chan interface{}, m.PrefetchMigrations)

	if n > 0 {
		go m.readUp(ctx, curVersion, n, ret)
	} else {
		go m.readDown(ctx, curVersion, -n, ret)
	}

	return m.unlockErr(ctx, m.runMigrations(ctx, ret))
}

// Up looks at the currently active migration version
// and will migrate all the way up (applying all up migrations).
func (m *Migrate) Up(ctx context.Context) (retErr error) {
	ctx, span := m.startSpan(ctx, "migrate.up", attribute.String("migrate.direction", "up"))
	defer func() { endSpan(span, retErr) }()

	if err := m.lock(ctx); err != nil {
		return err
	}

	curVersion, dirty, err := m.databaseDrv.Version(ctx)
	if err != nil {
		return m.unlockErr(ctx, err)
	}

	if dirty {
		return m.unlockErr(ctx, ErrDirty{curVersion})
	}

	ret := make(chan interface{}, m.PrefetchMigrations)

	go m.readUp(ctx, curVersion, -1, ret)
	return m.unlockErr(ctx, m.runMigrations(ctx, ret))
}

// Down looks at the currently active migration version
// and will migrate all the way down (applying all down migrations).
func (m *Migrate) Down(ctx context.Context) (retErr error) {
	ctx, span := m.startSpan(ctx, "migrate.down", attribute.String("migrate.direction", "down"))
	defer func() { endSpan(span, retErr) }()

	if err := m.lock(ctx); err != nil {
		return err
	}

	curVersion, dirty, err := m.databaseDrv.Version(ctx)
	if err != nil {
		return m.unlockErr(ctx, err)
	}

	if dirty {
		return m.unlockErr(ctx, ErrDirty{curVersion})
	}

	ret := make(chan interface{}, m.PrefetchMigrations)
	go m.readDown(ctx, curVersion, -1, ret)
	return m.unlockErr(ctx, m.runMigrations(ctx, ret))
}

// Drop deletes everything in the database.
func (m *Migrate) Drop(ctx context.Context) (retErr error) {
	ctx, span := m.startSpan(ctx, "migrate.drop")
	defer func() { endSpan(span, retErr) }()

	if err := m.lock(ctx); err != nil {
		return err
	}
	if err := m.databaseDrv.Drop(ctx); err != nil {
		return m.unlockErr(ctx, err)
	}
	return m.unlock(ctx)
}

// Run runs any migration provided by you against the database.
// It does not check any currently active version in database.
// Usually you don't need this function at all. Use Migrate,
// Steps, Up or Down instead.
func (m *Migrate) Run(ctx context.Context, migration ...*Migration) (retErr error) {
	if len(migration) == 0 {
		return ErrNoChange
	}

	ctx, span := m.startSpan(ctx, "migrate.run")
	defer func() { endSpan(span, retErr) }()

	if err := m.lock(ctx); err != nil {
		return err
	}

	curVersion, dirty, err := m.databaseDrv.Version(ctx)
	if err != nil {
		return m.unlockErr(ctx, err)
	}

	if dirty {
		return m.unlockErr(ctx, ErrDirty{curVersion})
	}

	ret := make(chan interface{}, m.PrefetchMigrations)

	go func() {
		defer close(ret)
		for _, migr := range migration {
			if m.PrefetchMigrations > 0 && migr.Body != nil {
				m.logVerbosePrintf("Start buffering %v\n", migr.LogString())
			} else {
				m.logVerbosePrintf("Scheduled %v\n", migr.LogString())
			}

			ret <- migr
			go m.bufferMigration(ctx, migr)
		}
	}()

	return m.unlockErr(ctx, m.runMigrations(ctx, ret))
}

// Force sets a migration version.
// It does not check any currently active version in database.
// It resets the dirty state to false.
func (m *Migrate) Force(ctx context.Context, version int) (retErr error) {
	if version < -1 {
		return ErrInvalidVersion
	}

	ctx, span := m.startSpan(ctx, "migrate.force", attribute.Int("migrate.version", version))
	defer func() { endSpan(span, retErr) }()

	if err := m.lock(ctx); err != nil {
		return err
	}

	if err := m.databaseDrv.SetVersion(ctx, version, false); err != nil {
		return m.unlockErr(ctx, err)
	}

	return m.unlock(ctx)
}

// Version returns the currently active migration version.
// If no migration has been applied, yet, it will return ErrNilVersion.
func (m *Migrate) Version(ctx context.Context) (version uint, dirty bool, retErr error) {
	ctx, span := m.startSpan(ctx, "migrate.version")
	defer func() { endSpan(span, retErr) }()

	v, d, err := m.databaseDrv.Version(ctx)
	if err != nil {
		return 0, false, err
	}

	if v == database.NilVersion {
		return 0, false, ErrNilVersion
	}

	return suint(v), d, nil
}

// read reads either up or down migrations from source `from` to `to`.
// Each migration is then written to the ret channel.
// If an error occurs during reading, that error is written to the ret channel, too.
// Once read is done reading it will close the ret channel.
func (m *Migrate) read(ctx context.Context, from int, to int, ret chan<- interface{}) {
	defer close(ret)

	// check if from version exists
	if from >= 0 {
		if err := m.versionExists(ctx, suint(from)); err != nil {
			ret <- err
			return
		}
	}

	// check if to version exists
	if to >= 0 {
		if err := m.versionExists(ctx, suint(to)); err != nil {
			ret <- err
			return
		}
	}

	// no change?
	if from == to {
		ret <- ErrNoChange
		return
	}

	if from < to {
		// it's going up
		// apply first migration if from is nil version
		if from == -1 {
			firstVersion, err := m.sourceDrv.First(ctx)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					ret <- ErrNoMigrationFiles
				} else {
					ret <- err
				}
				return
			}

			migr, err := m.newMigration(ctx, firstVersion, int(firstVersion))
			if err != nil {
				ret <- err
				return
			}

			ret <- migr
			go m.bufferMigration(ctx, migr)

			from = int(firstVersion)
		}

		// run until we reach target ...
		for from < to {
			if m.stop() {
				return
			}

			next, err := m.sourceDrv.Next(ctx, suint(from))
			if err != nil {
				ret <- err
				return
			}

			migr, err := m.newMigration(ctx, next, int(next))
			if err != nil {
				ret <- err
				return
			}

			ret <- migr
			go m.bufferMigration(ctx, migr)

			from = int(next)
		}

	} else {
		// it's going down
		// run until we reach target ...
		for from > to && from >= 0 {
			if m.stop() {
				return
			}

			prev, err := m.sourceDrv.Prev(ctx, suint(from))
			if errors.Is(err, os.ErrNotExist) && to == -1 {
				// apply nil migration
				migr, err := m.newMigration(ctx, suint(from), -1)
				if err != nil {
					ret <- err
					return
				}
				ret <- migr
				go m.bufferMigration(ctx, migr)

				return

			} else if err != nil {
				ret <- err
				return
			}

			migr, err := m.newMigration(ctx, suint(from), int(prev))
			if err != nil {
				ret <- err
				return
			}

			ret <- migr
			go m.bufferMigration(ctx, migr)

			from = int(prev)
		}
	}
}

// readUp reads up migrations from `from` limited by `limit`.
// limit can be -1, implying no limit and reading until there are no more migrations.
// Each migration is then written to the ret channel.
// If an error occurs during reading, that error is written to the ret channel, too.
// Once readUp is done reading it will close the ret channel.
func (m *Migrate) readUp(ctx context.Context, from int, limit int, ret chan<- interface{}) {
	defer close(ret)

	// check if from version exists
	if from >= 0 {
		if err := m.versionExists(ctx, suint(from)); err != nil {
			ret <- err
			return
		}
	}

	if limit == 0 {
		ret <- ErrNoChange
		return
	}

	count := 0
	for count < limit || limit == -1 {
		if m.stop() {
			return
		}

		// apply first migration if from is nil version
		if from == -1 {
			firstVersion, err := m.sourceDrv.First(ctx)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					ret <- ErrNoMigrationFiles
				} else {
					ret <- err
				}
				return
			}

			migr, err := m.newMigration(ctx, firstVersion, int(firstVersion))
			if err != nil {
				ret <- err
				return
			}

			ret <- migr
			go m.bufferMigration(ctx, migr)
			from = int(firstVersion)
			count++
			continue
		}

		// apply next migration
		next, err := m.sourceDrv.Next(ctx, suint(from))
		if errors.Is(err, os.ErrNotExist) {
			// no limit, but no migrations applied?
			if limit == -1 && count == 0 {
				ret <- ErrNoChange
				return
			}

			// no limit, reached end
			if limit == -1 {
				return
			}

			// reached end, and didn't apply any migrations
			if limit > 0 && count == 0 {
				ret <- os.ErrNotExist
				return
			}

			// applied less migrations than limit?
			if count < limit {
				ret <- ErrShortLimit{suint(limit - count)}
				return
			}
		}
		if err != nil {
			ret <- err
			return
		}

		migr, err := m.newMigration(ctx, next, int(next))
		if err != nil {
			ret <- err
			return
		}

		ret <- migr
		go m.bufferMigration(ctx, migr)
		from = int(next)
		count++
	}
}

// readDown reads down migrations from `from` limited by `limit`.
// limit can be -1, implying no limit and reading until there are no more migrations.
// Each migration is then written to the ret channel.
// If an error occurs during reading, that error is written to the ret channel, too.
// Once readDown is done reading it will close the ret channel.
func (m *Migrate) readDown(ctx context.Context, from int, limit int, ret chan<- interface{}) {
	defer close(ret)

	// check if from version exists
	if from >= 0 {
		if err := m.versionExists(ctx, suint(from)); err != nil {
			ret <- err
			return
		}
	}

	if limit == 0 {
		ret <- ErrNoChange
		return
	}

	// no change if already at nil version
	if from == -1 && limit == -1 {
		ret <- ErrNoChange
		return
	}

	// can't go over limit if already at nil version
	if from == -1 && limit > 0 {
		ret <- os.ErrNotExist
		return
	}

	count := 0
	for count < limit || limit == -1 {
		if m.stop() {
			return
		}

		prev, err := m.sourceDrv.Prev(ctx, suint(from))
		if errors.Is(err, os.ErrNotExist) {
			// no limit or haven't reached limit, apply "first" migration
			if limit == -1 || limit-count > 0 {
				firstVersion, err := m.sourceDrv.First(ctx)
				if err != nil {
					ret <- err
					return
				}

				migr, err := m.newMigration(ctx, firstVersion, -1)
				if err != nil {
					ret <- err
					return
				}
				ret <- migr
				go m.bufferMigration(ctx, migr)
				count++
			}

			if count < limit {
				ret <- ErrShortLimit{suint(limit - count)}
			}
			return
		}
		if err != nil {
			ret <- err
			return
		}

		migr, err := m.newMigration(ctx, suint(from), int(prev))
		if err != nil {
			ret <- err
			return
		}

		ret <- migr
		go m.bufferMigration(ctx, migr)
		from = int(prev)
		count++
	}
}

// runMigrations reads *Migration and error from a channel. Any other type
// sent on this channel will result in a panic. Each migration is then
// proxied to the database driver and run against the database.
// Before running a newly received migration it will check if it's supposed
// to stop execution because it might have received a stop signal on the
// GracefulStop channel.
func (m *Migrate) runMigrations(ctx context.Context, ret <-chan interface{}) error {
	for r := range ret {

		if m.stop() {
			return nil
		}

		switch r := r.(type) {
		case error:
			return r

		case *Migration:
			migr := r

			direction := "up"
			if migr.TargetVersion < int(migr.Version) {
				direction = "down"
			}
			migrAttrs := m.otelAttrsWith(
				attribute.Int64("migrate.version", int64(migr.Version)),
				attribute.Int("migrate.target_version", migr.TargetVersion),
				attribute.String("migrate.direction", direction),
				attribute.String("migrate.identifier", migr.Identifier),
			)
			baseAttrs := m.otelAttrsWith(
				attribute.String("migrate.direction", direction),
			)
			metricAttrs := metric.WithAttributes(baseAttrs...)
			childCtx, childSpan := m.otelTracer.Start(ctx, "migrate.run_migration",
				trace.WithSpanKind(trace.SpanKindInternal),
				trace.WithAttributes(migrAttrs...),
			)

			// set version with dirty state
			if err := m.databaseDrv.SetVersion(childCtx, migr.TargetVersion, true); err != nil {
				m.otelInstruments.migrationsFailed.Add(childCtx, 1, failureAttrs(baseAttrs, "set_version_dirty"))
				database.RecordSpanError(childSpan, err)
				childSpan.End()
				return err
			}

			runStart := time.Now()
			if migr.Body != nil {
				m.logVerbosePrintf("Read and execute %v\n", migr.LogString())
				if err := m.databaseDrv.Run(childCtx, migr.BufferedBody); err != nil {
					dur := time.Since(runStart).Seconds()
					m.otelInstruments.migrationRunDuration.Record(childCtx, dur, metricAttrs)
					m.otelInstruments.migrationsFailed.Add(childCtx, 1, failureAttrs(baseAttrs, "run"))
					database.RecordSpanError(childSpan, err)
					childSpan.End()
					return err
				}
				dur := time.Since(runStart).Seconds()
				m.otelInstruments.migrationRunDuration.Record(childCtx, dur, metricAttrs)
			}

			// set clean state
			if err := m.databaseDrv.SetVersion(childCtx, migr.TargetVersion, false); err != nil {
				m.otelInstruments.migrationsFailed.Add(childCtx, 1, failureAttrs(baseAttrs, "set_version_clean"))
				database.RecordSpanError(childSpan, err)
				childSpan.End()
				return err
			}

			m.otelInstruments.migrationsApplied.Add(childCtx, 1, metricAttrs)
			childSpan.End()

			endTime := time.Now()
			readTime := migr.FinishedReading.Sub(migr.StartedBuffering)
			runTime := endTime.Sub(migr.FinishedReading)

			// log either verbose or normal
			if m.Log != nil {
				if m.Log.Verbose() {
					m.logPrintf("Finished %v (read %v, ran %v)\n", migr.LogString(), readTime, runTime)
				} else {
					m.logPrintf("%v (%v)\n", migr.LogString(), readTime+runTime)
				}
			}

		default:
			return fmt.Errorf("unknown type: %T with value: %+v", r, r)
		}
	}
	return nil
}

// versionExists checks the source if either the up or down migration for
// the specified migration version exists.
func (m *Migrate) versionExists(ctx context.Context, version uint) (result error) {
	// try up migration first
	up, _, err := m.sourceDrv.ReadUp(ctx, version)
	if err == nil {
		defer func() {
			if errClose := up.Close(); errClose != nil {
				result = errors.Join(result, errClose)
			}
		}()
	}
	if errors.Is(err, os.ErrExist) {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// then try down migration
	down, _, err := m.sourceDrv.ReadDown(ctx, version)
	if err == nil {
		defer func() {
			if errClose := down.Close(); errClose != nil {
				result = errors.Join(result, errClose)
			}
		}()
	}
	if errors.Is(err, os.ErrExist) {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	err = fmt.Errorf("no migration found for version %d: %w", version, err)
	m.logErr(err)
	return err
}

// stop returns true if no more migrations should be run against the database
// because a stop signal was received on the GracefulStop channel.
// Calls are cheap and this function is not blocking.
func (m *Migrate) stop() bool {
	if m.isGracefulStop {
		return true
	}

	select {
	case <-m.GracefulStop:
		m.isGracefulStop = true
		return true

	default:
		return false
	}
}

// newMigration is a helper func that returns a *Migration for the
// specified version and targetVersion.
func (m *Migrate) newMigration(ctx context.Context, version uint, targetVersion int) (*Migration, error) {
	var migr *Migration

	if targetVersion >= int(version) {
		r, identifier, err := m.sourceDrv.ReadUp(ctx, version)
		if errors.Is(err, os.ErrNotExist) {
			// create "empty" migration
			migr, err = NewMigration(nil, "", version, targetVersion)
			if err != nil {
				return nil, err
			}

		} else if err != nil {
			return nil, err

		} else {
			// create migration from up source
			migr, err = NewMigration(r, identifier, version, targetVersion)
			if err != nil {
				return nil, err
			}
		}

	} else {
		r, identifier, err := m.sourceDrv.ReadDown(ctx, version)
		if errors.Is(err, os.ErrNotExist) {
			// create "empty" migration
			migr, err = NewMigration(nil, "", version, targetVersion)
			if err != nil {
				return nil, err
			}

		} else if err != nil {
			return nil, err

		} else {
			// create migration from down source
			migr, err = NewMigration(r, identifier, version, targetVersion)
			if err != nil {
				return nil, err
			}
		}
	}

	if m.PrefetchMigrations > 0 && migr.Body != nil {
		m.logVerbosePrintf("Start buffering %v\n", migr.LogString())
	} else {
		m.logVerbosePrintf("Scheduled %v\n", migr.LogString())
	}

	return migr, nil
}

// lock is a thread safe helper function to lock the database.
// It should be called as late as possible when running migrations.
func (m *Migrate) lock(ctx context.Context) error {
	m.isLockedMu.Lock()
	defer m.isLockedMu.Unlock()

	if m.isLocked {
		return ErrLocked
	}

	// create done channel, used in the timeout goroutine
	done := make(chan bool, 1)
	defer func() {
		done <- true
	}()

	// use errchan to signal error back to this context
	errchan := make(chan error, 2)

	// start timeout goroutine
	timeout := time.After(m.LockTimeout)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-timeout:
				errchan <- ErrLockTimeout
				return
			}
		}
	}()

	// now try to acquire the lock
	go func() {
		if err := m.databaseDrv.Lock(ctx); err != nil {
			errchan <- err
		} else {
			errchan <- nil
		}
	}()

	// wait until we either receive ErrLockTimeout or error from Lock operation
	start := time.Now()
	err := <-errchan
	m.otelInstruments.lockDuration.Record(ctx, time.Since(start).Seconds(), m.otelMetricAttrs)
	if err == nil {
		m.isLocked = true
		return nil
	}

	// A failed lock is the most common operational failure, so record why.
	errType := "_OTHER"
	switch {
	case errors.Is(err, ErrLockTimeout):
		errType = "timeout"
	case errors.Is(err, ErrLocked), errors.Is(err, database.ErrLocked):
		errType = "locked"
	}
	m.otelInstruments.lockFailures.Add(ctx, 1, metric.WithAttributes(
		m.otelAttrsWith(semconv.ErrorTypeKey.String(errType))...))
	return err
}

// bufferMigration reads a migration body from the source, emitting a span and
// metrics for it. The read is where remote sources (github, gitlab, s3, gcs)
// actually spend their time: source.read_up only covers obtaining the reader,
// so without this the download would be untraced.
func (m *Migrate) bufferMigration(ctx context.Context, migr *Migration) {
	ctx, span := m.startSpan(ctx, "migrate.buffer_migration",
		attribute.Int64("migrate.version", int64(migr.Version)),
		attribute.String("migrate.identifier", migr.Identifier),
	)
	defer span.End()

	start := time.Now()
	err := migr.Buffer()
	m.otelInstruments.migrationBufferDuration.Record(ctx, time.Since(start).Seconds(), m.otelMetricAttrs)

	if err != nil {
		otelSpanSetError(span, err)
		m.logErr(err)
		return
	}
	if migr.BytesRead > 0 {
		m.otelInstruments.migrationBytesRead.Add(ctx, migr.BytesRead, m.otelMetricAttrs)
		span.SetAttributes(attribute.Int64("migrate.bytes_read", migr.BytesRead))
	}
}

// unlock is a thread safe helper function to unlock the database.
// It should be called as early as possible when no more migrations are
// expected to be executed.
func (m *Migrate) unlock(ctx context.Context) error {
	m.isLockedMu.Lock()
	defer m.isLockedMu.Unlock()

	if err := m.databaseDrv.Unlock(ctx); err != nil {
		// BUG: Can potentially create a deadlock. Add a timeout.
		return err
	}

	m.isLocked = false
	return nil
}

// unlockErr calls unlock and returns a combined error
// if a prevErr is not nil.
func (m *Migrate) unlockErr(ctx context.Context, prevErr error) error {
	if err := m.unlock(ctx); err != nil {
		prevErr = errors.Join(prevErr, err)
	}
	return prevErr
}

// logPrintf writes to m.Log if not nil
func (m *Migrate) logPrintf(format string, v ...interface{}) {
	if m.Log != nil {
		m.Log.Printf(format, v...)
	}
}

// logVerbosePrintf writes to m.Log if not nil. Use for verbose logging output.
func (m *Migrate) logVerbosePrintf(format string, v ...interface{}) {
	if m.Log != nil && m.Log.Verbose() {
		m.Log.Printf(format, v...)
	}
}

// logErr writes error to m.Log if not nil
func (m *Migrate) logErr(err error) {
	if m.Log != nil {
		m.Log.Printf("error: %v", err)
	}
}
