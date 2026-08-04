// Package dbotel centralizes the OpenTelemetry configuration shared by the
// database/sql based database drivers.
package dbotel

import (
	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel/attribute"
)

// Options returns the otelsql options every database/sql driver should use.
//
// Query text is suppressed. Migration bodies routinely contain data — seed rows,
// backfills, tenant identifiers — and a full migration file attached to a span
// as db.query.text both leaks that data to the observability backend and bloats
// the span. This matches the redaction the migrate and database driver wrappers
// apply to errors; without it that redaction is pointless, because the
// statement would be recorded verbatim on the child span anyway.
//
// Pass the db.system.name attribute for the driver, e.g.
// semconv.DBSystemNamePostgreSQL.
func Options(system attribute.KeyValue, extra ...otelsql.Option) []otelsql.Option {
	opts := make([]otelsql.Option, 0, 2+len(extra))
	opts = append(opts,
		otelsql.WithAttributes(system),
		otelsql.WithSpanOptions(otelsql.SpanOptions{
			DisableQuery: true,
		}),
	)
	return append(opts, extra...)
}
