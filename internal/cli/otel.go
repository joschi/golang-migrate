package cli

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

// otelShutdownTimeout bounds how long the process waits to flush telemetry.
// Migrations are often run in CI, where hanging on an unreachable collector is
// worse than dropping the trace.
const otelShutdownTimeout = 5 * time.Second

// otelEnvVars are the environment variables that opt this process into
// exporting telemetry.
//
// Telemetry is off unless one of these is set. autoexport defaults to the OTLP
// exporter when OTEL_TRACES_EXPORTER is unset, so initializing it
// unconditionally would make every `migrate` invocation try to reach
// localhost:4317/4318 and add latency plus error noise for users who never
// asked for telemetry.
var otelEnvVars = []string{
	"OTEL_TRACES_EXPORTER",
	"OTEL_METRICS_EXPORTER",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
}

// otelEnabled reports whether telemetry export was requested.
func otelEnabled() bool {
	if os.Getenv("OTEL_SDK_DISABLED") == "true" {
		return false
	}
	for _, key := range otelEnvVars {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// setupOTel installs global trace and meter providers when telemetry export has
// been requested, and returns a shutdown function that flushes them. When no
// telemetry is requested it returns a no-op shutdown and leaves the global
// providers untouched, so migrate stays entirely offline.
func setupOTel(ctx context.Context, version string) func() {
	noop := func() {}
	if !otelEnabled() {
		return noop
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName("migrate"),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		// resource.New returns a usable resource alongside non-fatal errors
		// (e.g. schema URL conflicts), so report and continue.
		otel.Handle(err)
		if res == nil {
			res = resource.Default()
		}
	}

	var shutdowns []func(context.Context) error

	if spanExporter, err := autoexport.NewSpanExporter(ctx); err != nil {
		otel.Handle(err)
	} else if !autoexport.IsNoneSpanExporter(spanExporter) {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(spanExporter),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	if reader, err := autoexport.NewMetricReader(ctx); err != nil {
		otel.Handle(err)
	} else if !autoexport.IsNoneMetricReader(reader) {
		mp := metric.NewMeterProvider(
			metric.WithReader(reader),
			metric.WithResource(res),
		)
		otel.SetMeterProvider(mp)
		shutdowns = append(shutdowns, mp.Shutdown)
	}

	if len(shutdowns) == 0 {
		return noop
	}

	// OnceFunc because shutdown runs both from Main's defer and from the
	// log.fatal path, which calls os.Exit and so never reaches the defer.
	return sync.OnceFunc(func() {
		ctx, cancel := context.WithTimeout(context.Background(), otelShutdownTimeout)
		defer cancel()

		errs := make([]error, 0, len(shutdowns))
		for _, shutdown := range shutdowns {
			errs = append(errs, shutdown(ctx))
		}
		if err := errors.Join(errs...); err != nil {
			otel.Handle(err)
		}
	})
}
