package migrate

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/internal/otelconv"
	"github.com/golang-migrate/migrate/v4/source"
)

const (
	// instrumentationName is the instrumentation scope name used when creating
	// the tracer and meter.
	instrumentationName = "github.com/golang-migrate/migrate/v4"
)

// Option configures a Migrate instance.
type Option interface {
	apply(*config)
}

type config struct {
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
}

type optionFunc func(*config)

func (f optionFunc) apply(c *config) { f(c) }

// WithTracerProvider sets the TracerProvider used for tracing, including the
// database and source driver wrappers created internally. When unset, the
// global TracerProvider is used.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return optionFunc(func(c *config) {
		if tp != nil {
			c.tracerProvider = tp
		}
	})
}

// WithMeterProvider sets the MeterProvider used for metrics. When unset, the
// global MeterProvider is used.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return optionFunc(func(c *config) {
		if mp != nil {
			c.meterProvider = mp
		}
	})
}

func newConfig(opts []Option) config {
	cfg := config{
		tracerProvider: otel.GetTracerProvider(),
		meterProvider:  otel.GetMeterProvider(),
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	return cfg
}

// databaseOTelOptions returns the options to pass to database.NewOTelDriver so
// that the wrapper uses the same TracerProvider as the Migrate instance.
func (c config) databaseOTelOptions() []database.OTelOption {
	return []database.OTelOption{database.WithTracerProvider(c.tracerProvider)}
}

// sourceOTelOptions returns the options to pass to source.NewOTelDriver so that
// the wrapper uses the same TracerProvider as the Migrate instance.
func (c config) sourceOTelOptions() []source.OTelOption {
	return []source.OTelOption{source.WithTracerProvider(c.tracerProvider)}
}

// otelInstruments holds pre-created OTel metric instruments for a Migrate instance.
type otelInstruments struct {
	// migrationsApplied counts successfully applied migrations.
	migrationsApplied metric.Int64Counter

	// migrationsFailed counts failed migration applications.
	migrationsFailed metric.Int64Counter

	// migrationRunDuration records the execution duration of databaseDrv.Run per migration.
	migrationRunDuration metric.Float64Histogram

	// lockDuration records how long acquiring the database lock took.
	lockDuration metric.Float64Histogram

	// lockFailures counts failed lock acquisitions, including timeouts.
	lockFailures metric.Int64Counter

	// migrationBufferDuration records how long reading a migration body from
	// the source took.
	migrationBufferDuration metric.Float64Histogram

	// migrationBytesRead counts bytes read from the migration source.
	migrationBytesRead metric.Int64Counter
}

// newOtelInstruments creates metric instruments from the provided meter.
// On any instrument creation error, the OTel global error handler is invoked
// and the corresponding instrument is replaced with a no-op (the OTel API
// guarantees no-op instruments are returned on error, so callers are safe).
func newOtelInstruments(meter metric.Meter) otelInstruments {
	applied, err := meter.Int64Counter(
		"migrate.migrations.applied",
		metric.WithDescription("Number of migrations successfully applied."),
		metric.WithUnit("{migration}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	failed, err := meter.Int64Counter(
		"migrate.migrations.failed",
		metric.WithDescription("Number of migrations that failed to apply."),
		metric.WithUnit("{migration}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	duration, err := meter.Float64Histogram(
		"migrate.migration.run.duration",
		metric.WithDescription("Execution duration of a single migration run against the database."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10),
	)
	if err != nil {
		otel.Handle(err)
	}

	lockDuration, err := meter.Float64Histogram(
		"migrate.lock.duration",
		metric.WithDescription("Duration of database lock acquisition."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30),
	)
	if err != nil {
		otel.Handle(err)
	}

	lockFailures, err := meter.Int64Counter(
		"migrate.lock.failures",
		metric.WithDescription("Number of failed database lock acquisitions, including timeouts."),
		metric.WithUnit("{failure}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	bufferDuration, err := meter.Float64Histogram(
		"migrate.migration.buffer.duration",
		metric.WithDescription("Duration of reading a migration body from the source."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10),
	)
	if err != nil {
		otel.Handle(err)
	}

	bytesRead, err := meter.Int64Counter(
		"migrate.migration.source.read",
		metric.WithDescription("Bytes read from the migration source."),
		metric.WithUnit("By"),
	)
	if err != nil {
		otel.Handle(err)
	}

	return otelInstruments{
		migrationsApplied:       applied,
		migrationsFailed:        failed,
		migrationRunDuration:    duration,
		lockDuration:            lockDuration,
		lockFailures:            lockFailures,
		migrationBufferDuration: bufferDuration,
		migrationBytesRead:      bytesRead,
	}
}

// newTracer returns a tracer from cfg's TracerProvider.
func newTracer(cfg config) trace.Tracer {
	opts := []trace.TracerOption{trace.WithSchemaURL(otelconv.SchemaURL)}
	if v := otelconv.Version(); v != "" {
		opts = append(opts, trace.WithInstrumentationVersion(v))
	}
	return cfg.tracerProvider.Tracer(instrumentationName, opts...)
}

// newMeter returns a meter from cfg's MeterProvider.
func newMeter(cfg config) metric.Meter {
	opts := []metric.MeterOption{metric.WithSchemaURL(otelconv.SchemaURL)}
	if v := otelconv.Version(); v != "" {
		opts = append(opts, metric.WithInstrumentationVersion(v))
	}
	return cfg.meterProvider.Meter(instrumentationName, opts...)
}
