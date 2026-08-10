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
	opLock       = "lock"
	opUnlock     = "unlock"
	opRun        = "run"
	opSetVersion = "set_version"
	opVersion    = "get_version"
	opDrop       = "drop"
)

// OTelDriver wraps a Driver and adds OpenTelemetry CLIENT spans for each
// database operation. Obtain one via NewOTelDriver and pass it to
// NewWithDatabaseInstance or NewWithInstance.
type OTelDriver struct {
	driver     Driver
	driverName string
	tracer     trace.Tracer

	// migrationsTable is the db.collection.name target, empty when the wrapped
	// driver does not report one.
	migrationsTable string

	// spans holds the span name and attributes for each operation. Everything in
	// it is fixed once the driver is wrapped, so it is built once rather than on
	// every call.
	spans map[string]spanTemplate
}

// spanTemplate is the precomputed span name and attribute set for one operation.
type spanTemplate struct {
	name  string
	attrs []attribute.KeyValue
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

// NewOTelDriver wraps driver with OpenTelemetry instrumentation.
// driverName populates the db.system.name attribute on every span; registered
// driver schemes are mapped to their semantic convention value.
func NewOTelDriver(driver Driver, driverName string, opts ...OTelOption) Driver {
	table := migrationsTableOf(driver)
	return &OTelDriver{
		driver:          driver,
		driverName:      driverName,
		tracer:          otelconv.Tracer(newOTelConfig(opts).tracerProvider, tracerName),
		migrationsTable: table,
		spans:           newSpanTemplates(driverName, table),
	}
}

// newSpanTemplates precomputes the span name and attributes for every operation.
//
// Span names follow the database semantic conventions: "{db.operation.name}
// {target}", falling back to "{db.operation.name}" when there is no target. Only
// the operations that act on the migrations table get it as their target; run
// executes arbitrary migration SQL and drop targets the whole database, so
// naming either after the migrations table would misreport what it touches.
func newSpanTemplates(driverName, migrationsTable string) map[string]spanTemplate {
	system := semconv.DBSystemNameKey.String(otelconv.DBSystemName(driverName))

	templates := make(map[string]spanTemplate, 6)
	for _, op := range []struct {
		name             string
		targetsMigrTable bool
	}{
		{opLock, false},
		{opUnlock, false},
		{opRun, false},
		{opDrop, false},
		{opSetVersion, true},
		{opVersion, true},
	} {
		t := spanTemplate{
			name:  op.name,
			attrs: []attribute.KeyValue{system, semconv.DBOperationNameKey.String(op.name)},
		}
		if op.targetsMigrTable && migrationsTable != "" {
			t.name = op.name + " " + migrationsTable
			t.attrs = append(t.attrs, semconv.DBCollectionNameKey.String(migrationsTable))
		}
		templates[op.name] = t
	}
	return templates
}

// MigrationsTable forwards the wrapped driver's migrations table, so that
// instrumentation metadata survives wrapping.
func (d *OTelDriver) MigrationsTable() string {
	return d.migrationsTable
}

// startSpan starts a CLIENT span for operation from its precomputed template.
func (d *OTelDriver) startSpan(ctx context.Context, operation string, extra ...attribute.KeyValue) (context.Context, trace.Span) {
	t := d.spans[operation]

	attrs := t.attrs
	if len(extra) > 0 {
		attrs = make([]attribute.KeyValue, 0, len(t.attrs)+len(extra))
		attrs = append(attrs, t.attrs...)
		attrs = append(attrs, extra...)
	}

	return d.tracer.Start(ctx, t.name,
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

// errorType returns a low cardinality value for the error.type attribute,
// layering this package's lock sentinels over the shared classification.
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
	}
	return otelconv.ErrorType(err)
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
	table := migrationsTableOf(inner)
	return &OTelDriver{
		driver:          inner,
		driverName:      d.driverName,
		tracer:          d.tracer,
		migrationsTable: table,
		spans:           newSpanTemplates(d.driverName, table),
	}, nil
}

// Close delegates to the underlying driver without adding a span.
// Close is called once at teardown time and is not a recurring operation.
func (d *OTelDriver) Close(ctx context.Context) error {
	return d.driver.Close(ctx)
}

func (d *OTelDriver) Lock(ctx context.Context) error {
	ctx, span := d.startSpan(ctx, opLock)
	err := d.driver.Lock(ctx)
	endSpan(span, err)
	return err
}

func (d *OTelDriver) Unlock(ctx context.Context) error {
	ctx, span := d.startSpan(ctx, opUnlock)
	err := d.driver.Unlock(ctx)
	endSpan(span, err)
	return err
}

func (d *OTelDriver) Run(ctx context.Context, migration io.Reader) error {
	ctx, span := d.startSpan(ctx, opRun)
	err := d.driver.Run(ctx, migration)
	endSpan(span, err)
	return err
}

func (d *OTelDriver) SetVersion(ctx context.Context, version int, dirty bool) error {
	ctx, span := d.startSpan(ctx, opSetVersion,
		attribute.Int("migrate.version", version),
		attribute.Bool("migrate.dirty", dirty),
	)
	err := d.driver.SetVersion(ctx, version, dirty)
	endSpan(span, err)
	return err
}

func (d *OTelDriver) Version(ctx context.Context) (int, bool, error) {
	ctx, span := d.startSpan(ctx, opVersion)
	version, dirty, err := d.driver.Version(ctx)
	endSpan(span, err)
	return version, dirty, err
}

func (d *OTelDriver) Drop(ctx context.Context) error {
	ctx, span := d.startSpan(ctx, opDrop)
	err := d.driver.Drop(ctx)
	endSpan(span, err)
	return err
}
