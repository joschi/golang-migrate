package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/golang-migrate/migrate/v4/database"
	dStub "github.com/golang-migrate/migrate/v4/database/stub"
	sStub "github.com/golang-migrate/migrate/v4/source/stub"
)

// secretSQL stands in for a migration body containing data that must not be
// shipped to an observability backend.
const secretSQL = "INSERT INTO users (email) VALUES ('alice@example.com');"

// failingDriver returns a database.Error carrying migration SQL from Run.
type failingDriver struct {
	database.Driver
}

func (d *failingDriver) Run(ctx context.Context, migration io.Reader) error {
	return database.Error{
		Line:    3,
		Query:   []byte(secretSQL),
		Err:     "migration failed",
		OrigErr: errors.New("syntax error"),
	}
}

// newRedactionTest wires a Migrate instance with an explicitly injected
// TracerProvider (no global state) whose database driver always fails.
func newRedactionTest(t *testing.T) (*Migrate, *tracetest.InMemoryExporter) {
	t.Helper()

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx := context.Background()
	srcDrv, err := (&sStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)
	srcDrv.(*sStub.Stub).Migrations = sourceStubMigrations

	dbDrv, err := (&dStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)

	m, err := NewWithInstance(ctx, "stub", srcDrv, "stub", &failingDriver{Driver: dbDrv},
		WithTracerProvider(tp))
	require.NoError(t, err)
	return m, exp
}

// TestOtelMigrationSQLIsNotLeaked is the regression test for migration bodies
// reaching telemetry. Before the fix the status description was scrubbed but
// span.RecordError still recorded the full error, so exception.message carried
// the SQL on every span in the trace.
func TestOtelMigrationSQLIsNotLeaked(t *testing.T) {
	m, exp := newRedactionTest(t)

	err := m.Up(context.Background())
	require.Error(t, err)
	// The error returned to the caller keeps the query: only telemetry is redacted.
	assert.Contains(t, err.Error(), secretSQL, "caller-facing error must be unchanged")

	snaps := exp.GetSpans().Snapshots()
	require.NotEmpty(t, snaps)

	var checkedStatuses, checkedEvents int
	for _, s := range snaps {
		assert.NotContains(t, s.Status().Description, secretSQL,
			"span %q status description leaks migration SQL", s.Name())
		if s.Status().Description != "" {
			checkedStatuses++
		}
		for _, ev := range s.Events() {
			for _, a := range ev.Attributes {
				assert.NotContains(t, a.Value.String(), secretSQL,
					"span %q event %q attribute %q leaks migration SQL", s.Name(), ev.Name, a.Key)
				checkedEvents++
			}
		}
		for _, a := range s.Attributes() {
			assert.NotContains(t, a.Value.String(), secretSQL,
				"span %q attribute %q leaks migration SQL", s.Name(), a.Key)
		}
	}
	// Guard against the test passing because nothing was recorded at all.
	assert.Positive(t, checkedStatuses, "expected at least one error status")
	assert.Positive(t, checkedEvents, "expected at least one exception event")
}

// TestOtelErrorTypeAttribute checks that failures carry error.type.
func TestOtelErrorTypeAttribute(t *testing.T) {
	m, exp := newRedactionTest(t)
	require.Error(t, m.Up(context.Background()))

	var found bool
	for _, s := range exp.GetSpans().Snapshots() {
		if s.Name() != "db.run" {
			continue
		}
		for _, a := range s.Attributes() {
			if a.Key == semconv.ErrorTypeKey {
				found = true
			}
		}
	}
	assert.True(t, found, "db.run span must carry error.type on failure")
}

// TestOtelNoOkStatus verifies no span status is set to Ok: the specification
// reserves Ok for the application, not instrumentation libraries.
func TestOtelNoOkStatus(t *testing.T) {
	m, exp := setupOtelTopologyTest(t)
	require.NoError(t, m.Up(context.Background()))

	for _, s := range exp.GetSpans().Snapshots() {
		assert.NotEqual(t, codes.Ok, s.Status().Code,
			"span %q must not set status Ok", s.Name())
	}
}

// TestOtelDBSystemNameIsSemconv verifies the driver scheme is mapped to the
// semantic convention value, so that the wrapper and the underlying
// database/sql instrumentation agree within one trace.
func TestOtelDBSystemNameIsSemconv(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx := context.Background()
	srcDrv, err := (&sStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)
	srcDrv.(*sStub.Stub).Migrations = sourceStubMigrations
	dbDrv, err := (&dStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)

	// "sqlite3" is the registered scheme; semconv spells the system "sqlite".
	m, err := NewWithInstance(ctx, "stub", srcDrv, "sqlite3", dbDrv, WithTracerProvider(tp))
	require.NoError(t, err)
	require.NoError(t, m.Up(ctx))

	var checked int
	for _, s := range exp.GetSpans().Snapshots() {
		for _, a := range s.Attributes() {
			if a.Key == semconv.DBSystemNameKey {
				assert.Equal(t, "sqlite", a.Value.AsString(),
					"span %q reports a non-semconv db.system.name", s.Name())
				checked++
			}
		}
	}
	assert.Positive(t, checked, "expected db.system.name on some spans")
}

// TestOtelBufferMigrationSpan verifies the source body read is covered by a
// span. source.read_up only covers obtaining the reader, so for remote sources
// the download would otherwise be untraced.
func TestOtelBufferMigrationSpan(t *testing.T) {
	m, exp := setupOtelTopologyTest(t)
	require.NoError(t, m.Up(context.Background()))

	names := map[string]bool{}
	byID := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, s := range exp.GetSpans().Snapshots() {
		names[s.Name()] = true
		byID[s.SpanContext().SpanID()] = s
	}
	require.True(t, names["migrate.buffer_migration"], "expected a buffering span")

	for _, s := range exp.GetSpans().Snapshots() {
		if s.Name() != "migrate.buffer_migration" {
			continue
		}
		parent, ok := byID[s.Parent().SpanID()]
		require.True(t, ok, "migrate.buffer_migration must be parented")
		assert.True(t, strings.HasPrefix(parent.Name(), "migrate."),
			"unexpected parent %q", parent.Name())
	}
}

// TestOtelProviderIsInjectable verifies no global provider is needed, which the
// wrappers previously required.
func TestOtelProviderIsInjectable(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx := context.Background()
	srcDrv, err := (&sStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)
	srcDrv.(*sStub.Stub).Migrations = sourceStubMigrations
	dbDrv, err := (&dStub.Stub{}).Open(ctx, "stub://")
	require.NoError(t, err)

	m, err := NewWithInstance(ctx, "stub", srcDrv, "stub", dbDrv, WithTracerProvider(tp))
	require.NoError(t, err)
	require.NoError(t, m.Up(ctx))

	snaps := exp.GetSpans().Snapshots()
	require.NotEmpty(t, snaps, "injected TracerProvider received no spans")

	// Spans from all three scopes must land in the injected provider.
	scopes := map[string]bool{}
	for _, s := range snaps {
		scopes[s.InstrumentationScope().Name] = true
	}
	for _, want := range []string{
		"github.com/golang-migrate/migrate/v4",
		"github.com/golang-migrate/migrate/v4/database",
		"github.com/golang-migrate/migrate/v4/source",
	} {
		assert.True(t, scopes[want], "no spans from scope %q; got %v", want, scopes)
	}
}

// TestOtelVersionAndCloseAreTraced guards against unparented root spans from
// the driver calls these methods make.
func TestOtelVersionAndCloseAreTraced(t *testing.T) {
	m, exp := setupOtelTopologyTest(t)
	require.NoError(t, m.Up(context.Background()))
	exp.Reset()

	_, _, err := m.Version(context.Background())
	require.NoError(t, err)

	var found bool
	for _, s := range exp.GetSpans().Snapshots() {
		if s.Name() == "migrate.version" {
			found = true
		}
		if s.Name() == "db.version" {
			assert.True(t, s.Parent().SpanID().IsValid(),
				"db.version must not be an orphan root span")
		}
	}
	assert.True(t, found, "Version must emit a span")

	exp.Reset()
	srcErr, dbErr := m.Close(context.Background())
	require.NoError(t, srcErr)
	require.NoError(t, dbErr)

	found = false
	for _, s := range exp.GetSpans().Snapshots() {
		if s.Name() == "migrate.close" {
			found = true
		}
	}
	assert.True(t, found, "Close must emit a span")
}

func TestRedactError(t *testing.T) {
	orig := errors.New("syntax error")
	dbErr := database.Error{Line: 7, Query: []byte(secretSQL), Err: "boom", OrigErr: orig}

	redacted := database.RedactError(fmt.Errorf("wrapped: %w", dbErr))
	assert.NotContains(t, redacted.Error(), secretSQL)
	assert.Contains(t, redacted.Error(), "boom")
	assert.ErrorIs(t, redacted, orig, "redaction must preserve errors.Is")

	plain := errors.New("not a database error")
	assert.Equal(t, plain, database.RedactError(plain))
	assert.NoError(t, database.RedactError(nil))
}
