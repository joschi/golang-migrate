package database

import (
	"context"
	"errors"
	"io"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/golang-migrate/migrate/v4/internal/otelconv"
)

const tracerName = "github.com/golang-migrate/migrate/v4/database"

// Operation names reported as db.operation.name. They describe the migration
// bookkeeping operation rather than a single statement, since one call may run
// several statements.
const (
	opLock       = "LOCK"
	opUnlock     = "UNLOCK"
	opRun        = "RUN MIGRATION"
	opSetVersion = "SET VERSION"
	opVersion    = "GET VERSION"
	opDrop       = "DROP"
)

// OTelDriver wraps a Driver and adds OpenTelemetry CLIENT spans for each
// database operation. Obtain one via NewOTelDriver and pass it to
// NewWithDatabaseInstance or NewWithInstance.
type OTelDriver struct {
	driver     Driver
	driverName string
	tracer     trace.Tracer
	opts       []OTelOption
}

// OTelOption configures the OpenTelemetry driver wrapper.
type OTelOption interface {
	applyOTel(*otelConfig)
}

type otelConfig struct {
	tracerProvider trace.TracerProvider
}

type otelOptionFunc func(*otelConfig)

func (f otelOptionFunc) applyOTel(c *otelConfig) { f(c) }

// WithTracerProvider sets the TracerProvider used by the wrapper. When unset,
// the global TracerProvider is used.
func WithTracerProvider(tp trace.TracerProvider) OTelOption {
	return otelOptionFunc(func(c *otelConfig) {
		if tp != nil {
			c.tracerProvider = tp
		}
	})
}

func newOTelConfig(opts []OTelOption) otelConfig {
	cfg := otelConfig{tracerProvider: otel.GetTracerProvider()}
	for _, opt := range opts {
		opt.applyOTel(&cfg)
	}
	return cfg
}

func newTracer(cfg otelConfig) trace.Tracer {
	tracerOpts := []trace.TracerOption{trace.WithSchemaURL(otelconv.SchemaURL)}
	if v := otelconv.Version(); v != "" {
		tracerOpts = append(tracerOpts, trace.WithInstrumentationVersion(v))
	}
	return cfg.tracerProvider.Tracer(tracerName, tracerOpts...)
}

// NewOTelDriver wraps driver with OpenTelemetry instrumentation.
// driverName populates the db.system.name attribute on every span; registered
// driver schemes are mapped to their semantic convention value.
func NewOTelDriver(driver Driver, driverName string, opts ...OTelOption) Driver {
	return &OTelDriver{
		driver:     driver,
		driverName: driverName,
		tracer:     newTracer(newOTelConfig(opts)),
		opts:       opts,
	}
}

func (d *OTelDriver) attrs(operation string) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.DBSystemNameKey.String(otelconv.DBSystemName(d.driverName)),
		semconv.DBOperationNameKey.String(operation),
	}
}

func (d *OTelDriver) startSpan(ctx context.Context, name, operation string, extra ...attribute.KeyValue) (context.Context, trace.Span) {
	attrs := append(d.attrs(operation), extra...)
	return d.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
}

// RecordSpanError records err on span and sets an error status. The migration
// query carried by Error is stripped first, so that neither the status
// description nor the recorded exception event leaks the migration body.
func RecordSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}
	redacted := RedactError(err)
	span.RecordError(redacted)
	span.SetAttributes(semconv.ErrorTypeKey.String(errorType(err)))
	span.SetStatus(codes.Error, redacted.Error())
}

// errorType returns a low cardinality value for the error.type attribute.
func errorType(err error) string {
	var dbErr Error
	if errors.As(err, &dbErr) && dbErr.OrigErr != nil {
		err = dbErr.OrigErr
	}
	switch {
	case errors.Is(err, ErrLocked):
		return "locked"
	case errors.Is(err, ErrNotLocked):
		return "not_locked"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "_OTHER"
}

func endSpan(span trace.Span, err error) {
	RecordSpanError(span, err)
	span.End()
}

// Unwrap returns the underlying Driver. It follows the same convention as
// errors.Unwrap and allows callers (e.g. tests) to access the inner driver.
func (d *OTelDriver) Unwrap() Driver {
	return d.driver
}

// Open opens the underlying driver and returns it wrapped, so that
// instrumentation is not silently lost when Open is called on a wrapped driver.
// No span is emitted: Open is called once at construction time and is not a
// recurring operation.
func (d *OTelDriver) Open(ctx context.Context, url string) (Driver, error) {
	inner, err := d.driver.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	return NewOTelDriver(inner, d.driverName, d.opts...), nil
}

// Close delegates to the underlying driver without adding a span.
// Close is called once at teardown time and is not a recurring operation.
func (d *OTelDriver) Close(ctx context.Context) error {
	return d.driver.Close(ctx)
}

func (d *OTelDriver) Lock(ctx context.Context) error {
	ctx, span := d.startSpan(ctx, "db.lock", opLock)
	err := d.driver.Lock(ctx)
	endSpan(span, err)
	return err
}

func (d *OTelDriver) Unlock(ctx context.Context) error {
	ctx, span := d.startSpan(ctx, "db.unlock", opUnlock)
	err := d.driver.Unlock(ctx)
	endSpan(span, err)
	return err
}

func (d *OTelDriver) Run(ctx context.Context, migration io.Reader) error {
	ctx, span := d.startSpan(ctx, "db.run", opRun)
	err := d.driver.Run(ctx, migration)
	endSpan(span, err)
	return err
}

func (d *OTelDriver) SetVersion(ctx context.Context, version int, dirty bool) error {
	ctx, span := d.startSpan(ctx, "db.set_version", opSetVersion,
		attribute.Int("migrate.version", version),
		attribute.Bool("migrate.dirty", dirty),
	)
	err := d.driver.SetVersion(ctx, version, dirty)
	endSpan(span, err)
	return err
}

func (d *OTelDriver) Version(ctx context.Context) (int, bool, error) {
	ctx, span := d.startSpan(ctx, "db.version", opVersion)
	version, dirty, err := d.driver.Version(ctx)
	endSpan(span, err)
	return version, dirty, err
}

func (d *OTelDriver) Drop(ctx context.Context) error {
	ctx, span := d.startSpan(ctx, "db.drop", opDrop)
	err := d.driver.Drop(ctx)
	endSpan(span, err)
	return err
}
