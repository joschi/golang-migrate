package database_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/golang-migrate/migrate/v5/database"
	dStub "github.com/golang-migrate/migrate/v5/database/stub"
)

// setGlobalTP sets tp as the global TracerProvider for the duration of the
// test and restores the previous provider in t.Cleanup.
func setGlobalTP(t *testing.T, tp *sdktrace.TracerProvider) {
	t.Helper()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
}

// newTestDriver installs tp as the global provider, then returns an OTelDriver
// wrapping a fresh in-memory stub. The inner *dStub.Stub is also returned for
// state inspection.
func newTestDriver(t *testing.T, tp *sdktrace.TracerProvider) database.Driver {
	t.Helper()
	setGlobalTP(t, tp)

	ctx := context.Background()
	inner, err := (&dStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)

	return database.NewOTelDriver(inner, "testdb")
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.Name()
	}
	return out
}

func findSpan(spans []sdktrace.ReadOnlySpan, name string) (sdktrace.ReadOnlySpan, bool) {
	for _, s := range spans {
		if s.Name() == name {
			return s, true
		}
	}
	return nil, false
}

func attrVal(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

func TestOTelDriver_Unwrap(t *testing.T) {
	ctx := context.Background()
	inner, err := (&dStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)
	drv := database.NewOTelDriver(inner, "stub")
	assert.Equal(t, inner, drv.(*database.OTelDriver).Unwrap())
}

func TestOTelDriver_SpanNamesAndKind(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	drv := newTestDriver(t, tp)
	ctx := context.Background()

	require.NoError(t, drv.Lock(ctx))
	require.NoError(t, drv.Unlock(ctx))
	_, _, err := drv.Version(ctx)
	require.NoError(t, err)
	require.NoError(t, drv.SetVersion(ctx, 1, true))
	require.NoError(t, drv.SetVersion(ctx, 1, false))
	require.NoError(t, drv.Run(ctx, strings.NewReader("sql")))
	require.NoError(t, drv.Drop(ctx))

	snaps := exp.GetSpans().Snapshots()
	names := spanNames(snaps)

	assert.Contains(t, names, "lock")
	assert.Contains(t, names, "unlock")
	assert.Contains(t, names, "get_version")
	assert.Contains(t, names, "set_version")
	assert.Contains(t, names, "run")
	assert.Contains(t, names, "drop")

	// All emitted spans must be CLIENT kind.
	for _, s := range snaps {
		assert.Equal(t, trace.SpanKindClient, s.SpanKind(),
			"span %q: expected SpanKindClient", s.Name())
	}
}

func TestOTelDriver_DbSystemAttribute(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	drv := newTestDriver(t, tp)
	ctx := context.Background()

	require.NoError(t, drv.Lock(ctx))

	snaps := exp.GetSpans().Snapshots()
	require.Len(t, snaps, 1)
	v, ok := attrVal(snaps[0], "db.system.name")
	require.True(t, ok, "db.system.name attribute must be present on lock span")
	assert.Equal(t, "testdb", v)
}

func TestOTelDriver_SetVersionAttributes(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	drv := newTestDriver(t, tp)
	ctx := context.Background()

	require.NoError(t, drv.SetVersion(ctx, 42, true))

	snaps := exp.GetSpans().Snapshots()
	span, ok := findSpan(snaps, "set_version")
	require.True(t, ok)

	// migrate.version must be 42.
	for _, kv := range span.Attributes() {
		if string(kv.Key) == "migrate.version" {
			assert.Equal(t, int64(42), kv.Value.AsInt64())
		}
		if string(kv.Key) == "migrate.dirty" {
			assert.True(t, kv.Value.AsBool())
		}
	}
}

func TestOTelDriver_NoSpanForOpenAndClose(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	drv := newTestDriver(t, tp)
	ctx := context.Background()

	// Open and Close must not emit spans.
	_, _ = drv.Open(ctx, "stub://")
	_ = drv.Close(ctx)

	assert.Empty(t, exp.GetSpans().Snapshots(), "Open and Close must not emit spans")
}

// tableStub is a Driver that reports a migrations table.
type tableStub struct {
	database.Driver
	table string
}

func (t *tableStub) MigrationsTable() string { return t.table }

// TestOTelDriver_SemconvSpanNames checks the span naming required by the
// database semantic conventions: "{db.operation.name} {target}" when a target is
// known, "{db.operation.name}" otherwise.
func TestOTelDriver_SemconvSpanNames(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx := context.Background()
	inner, err := (&dStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)

	d := database.NewOTelDriver(&tableStub{Driver: inner, table: "schema_migrations"},
		"sqlite3", database.WithTracerProvider(tp))

	require.NoError(t, d.Lock(ctx))
	_, _, err = d.Version(ctx)
	require.NoError(t, err)
	require.NoError(t, d.SetVersion(ctx, 1, false))
	require.NoError(t, d.Run(ctx, strings.NewReader("SELECT 1;")))
	require.NoError(t, d.Drop(ctx))
	require.NoError(t, d.Unlock(ctx))

	names := spanNames(exp.GetSpans().Snapshots())

	// Operations acting on the migrations table carry it as the target.
	assert.Contains(t, names, "get_version schema_migrations")
	assert.Contains(t, names, "set_version schema_migrations")

	// Run executes arbitrary migration SQL and Drop targets the whole database,
	// so neither may claim the migrations table as its target.
	assert.Contains(t, names, "run")
	assert.Contains(t, names, "drop")
	assert.Contains(t, names, "lock")
	assert.Contains(t, names, "unlock")
	for _, n := range names {
		if strings.HasPrefix(n, "run ") || strings.HasPrefix(n, "drop ") ||
			strings.HasPrefix(n, "lock ") || strings.HasPrefix(n, "unlock ") {
			t.Errorf("span %q must not claim a collection target", n)
		}
	}

	// db.collection.name is set exactly on the spans that name it.
	for _, s := range exp.GetSpans().Snapshots() {
		var collection string
		for _, a := range s.Attributes() {
			if a.Key == semconv.DBCollectionNameKey {
				collection = a.Value.AsString()
			}
		}
		wantCollection := s.Name() == "get_version schema_migrations" ||
			s.Name() == "set_version schema_migrations"
		if wantCollection {
			assert.Equal(t, "schema_migrations", collection, "span %q", s.Name())
		} else {
			assert.Empty(t, collection, "span %q must not set db.collection.name", s.Name())
		}
	}
}

// TestOTelDriver_SpanNamesWithoutTable covers a driver that reports no table:
// span names fall back to the operation alone.
func TestOTelDriver_SpanNamesWithoutTable(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx := context.Background()
	inner, err := (&dStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)
	d := database.NewOTelDriver(inner, "stub", database.WithTracerProvider(tp))

	_, _, err = d.Version(ctx)
	require.NoError(t, err)

	names := spanNames(exp.GetSpans().Snapshots())
	assert.Contains(t, names, "get_version")
	for _, s := range exp.GetSpans().Snapshots() {
		for _, a := range s.Attributes() {
			assert.NotEqual(t, semconv.DBCollectionNameKey, a.Key,
				"db.collection.name must be absent when the driver reports no table")
		}
	}
}

// TestOTelDriver_MigrationsTableSurvivesWrapping guards the forwarding that lets
// a double-wrapped or re-Opened driver keep reporting its table.
func TestOTelDriver_MigrationsTableSurvivesWrapping(t *testing.T) {
	ctx := context.Background()
	inner, err := (&dStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)

	once := database.NewOTelDriver(&tableStub{Driver: inner, table: "custom_table"}, "sqlite3")
	twice := database.NewOTelDriver(once, "sqlite3")

	tabler, ok := twice.(database.MigrationsTabler)
	require.True(t, ok, "wrapped driver must still report its migrations table")
	assert.Equal(t, "custom_table", tabler.MigrationsTable())
}
