package source

import (
	"context"
	"errors"
	"io"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/golang-migrate/migrate/v5/internal/otelconv"
)

const tracerName = "github.com/golang-migrate/migrate/v5/source"

// OTelDriver wraps a Driver and adds OpenTelemetry INTERNAL spans for ReadUp
// and ReadDown. Obtain one via NewOTelDriver and pass it to
// NewWithSourceInstance or NewWithInstance.
type OTelDriver struct {
	driver     Driver
	sourceName string
	tracer     trace.Tracer

	// sourceAttr is the migrate.source attribute, fixed once the driver is
	// wrapped.
	sourceAttr attribute.KeyValue
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
// sourceName populates the migrate.source attribute on every span.
func NewOTelDriver(driver Driver, sourceName string, opts ...OTelOption) Driver {
	return &OTelDriver{
		driver:     driver,
		sourceName: sourceName,
		tracer:     otelconv.Tracer(newOTelConfig(opts).tracerProvider, tracerName),
		sourceAttr: attribute.String("migrate.source", sourceName),
	}
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
	return &OTelDriver{
		driver:     inner,
		sourceName: d.sourceName,
		tracer:     d.tracer,
		sourceAttr: d.sourceAttr,
	}, nil
}

// Close delegates to the underlying driver without adding a span.
// Close is called once at teardown time and is not a recurring operation.
func (d *OTelDriver) Close(ctx context.Context) error {
	return d.driver.Close(ctx)
}

// First delegates without a span — in-memory map lookup in every driver.
func (d *OTelDriver) First(ctx context.Context) (uint, error) {
	return d.driver.First(ctx)
}

// Prev delegates without a span — in-memory map lookup in every driver.
func (d *OTelDriver) Prev(ctx context.Context, version uint) (uint, error) {
	return d.driver.Prev(ctx, version)
}

// Next delegates without a span — in-memory map lookup in every driver.
func (d *OTelDriver) Next(ctx context.Context, version uint) (uint, error) {
	return d.driver.Next(ctx, version)
}

// endReadSpan finishes a read span. A missing migration (os.ErrNotExist) is a
// normal control flow signal for callers such as versionExists and is therefore
// not recorded as an error.
func endReadSpan(span trace.Span, err error) {
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		span.RecordError(err)
		span.SetAttributes(semconv.ErrorTypeKey.String(otelconv.ErrorType(err)))
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func (d *OTelDriver) ReadUp(ctx context.Context, version uint) (io.ReadCloser, string, error) {
	ctx, span := d.tracer.Start(ctx, "source.read_up",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(d.sourceAttr, attribute.Int64("migrate.version", int64(version))),
	)
	r, identifier, err := d.driver.ReadUp(ctx, version)
	endReadSpan(span, err)
	return r, identifier, err
}

func (d *OTelDriver) ReadDown(ctx context.Context, version uint) (io.ReadCloser, string, error) {
	ctx, span := d.tracer.Start(ctx, "source.read_down",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(d.sourceAttr, attribute.Int64("migrate.version", int64(version))),
	)
	r, identifier, err := d.driver.ReadDown(ctx, version)
	endReadSpan(span, err)
	return r, identifier, err
}
