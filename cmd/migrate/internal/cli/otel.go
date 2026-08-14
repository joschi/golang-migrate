package cli

import (
	"context"
	"errors"
	"os"
	"strings"
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

// otlpEndpointEnvVars configure an OTLP destination. Setting one opts a signal
// in even when its OTEL_*_EXPORTER variable is unset, since autoexport then
// defaults to OTLP anyway.
var otlpEndpointEnvVars = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
}

// signalRequested reports whether export of one signal was asked for, where
// exporterEnvVar is that signal's OTEL_*_EXPORTER variable.
//
// The check is per signal on purpose. autoexport defaults to the OTLP exporter
// when a signal's variable is unset, so treating "any OTEL_* variable is set" as
// a single process-wide switch would start an OTLP exporter for the *other*
// signal too: `OTEL_TRACES_EXPORTER=console` alone would quietly open a
// connection to localhost:4318 to ship metrics.
func signalRequested(exporterEnvVar string) bool {
	if sdkDisabled() {
		return false
	}
	if os.Getenv(exporterEnvVar) != "" {
		return true
	}
	for _, key := range otlpEndpointEnvVars {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// sdkDisabled reports whether OTEL_SDK_DISABLED turns telemetry off. The
// specification defines it as a boolean, so the value is matched
// case-insensitively.
func sdkDisabled() bool {
	return strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true")
}

// setupOTel installs global trace and meter providers for the signals whose
// export was requested, and returns a shutdown function that flushes them. When
// no telemetry is requested it returns a no-op shutdown and leaves the global
// providers untouched, so migrate stays entirely offline.
func setupOTel(ctx context.Context, version string) func() {
	tracesOn := signalRequested("OTEL_TRACES_EXPORTER")
	metricsOn := signalRequested("OTEL_METRICS_EXPORTER")
	if !tracesOn && !metricsOn {
		return func() {}
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
	}

	var shutdowns []func(context.Context) error

	if tracesOn {
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
	}

	if metricsOn {
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
