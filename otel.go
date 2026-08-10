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

// counter creates an Int64Counter, reporting any creation error to the global
// OTel error handler. The OTel API guarantees a usable no-op instrument on
// error, so the result is always safe to use.
func counter(meter metric.Meter, name, description, unit string) metric.Int64Counter {
	c, err := meter.Int64Counter(name,
		metric.WithDescription(description),
		metric.WithUnit(unit),
	)
	if err != nil {
		otel.Handle(err)
	}
	return c
}

// histogram creates a Float64Histogram of seconds with explicit bucket
// boundaries, reporting any creation error as counter does.
func histogram(meter metric.Meter, name, description string, boundaries ...float64) metric.Float64Histogram {
	h, err := meter.Float64Histogram(name,
		metric.WithDescription(description),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(boundaries...),
	)
	if err != nil {
		otel.Handle(err)
	}
	return h
}

// newOtelInstruments creates the metric instruments from the provided meter.
func newOtelInstruments(meter metric.Meter) otelInstruments {
	return otelInstruments{
		migrationsApplied: counter(meter,
			"migrate.migrations.applied",
			"Number of migrations successfully applied.",
			"{migration}"),
		migrationsFailed: counter(meter,
			"migrate.migrations.failed",
			"Number of migrations that failed to apply.",
			"{migration}"),
		migrationRunDuration: histogram(meter,
			"migrate.migration.run.duration",
			"Execution duration of a single migration run against the database.",
			0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10),
		lockDuration: histogram(meter,
			"migrate.lock.duration",
			"Duration of database lock acquisition.",
			0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30),
		lockFailures: counter(meter,
			"migrate.lock.failures",
			"Number of failed database lock acquisitions, including timeouts.",
			"{failure}"),
		migrationBufferDuration: histogram(meter,
			"migrate.migration.buffer.duration",
			"Duration of reading a migration body from the source.",
			0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10),
		migrationBytesRead: counter(meter,
			"migrate.migration.source.read",
			"Bytes read from the migration source.",
			"By"),
	}
}

// newTracer returns a tracer from cfg's TracerProvider.
func newTracer(cfg config) trace.Tracer {
	return otelconv.Tracer(cfg.tracerProvider, instrumentationName)
}

// newMeter returns a meter from cfg's MeterProvider.
func newMeter(cfg config) metric.Meter {
	return otelconv.Meter(cfg.meterProvider, instrumentationName)
}
