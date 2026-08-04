// Package otelconv holds OpenTelemetry conventions shared by the migrate
// package and the database/source driver wrappers.
package otelconv

import (
	"runtime/debug"
	"sync"

	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

// SchemaURL is the semantic convention schema URL that the emitted telemetry
// conforms to. It is reported on every instrumentation scope.
const SchemaURL = semconv.SchemaURL

// modulePath is the module whose version is reported as the instrumentation
// scope version.
const modulePath = "github.com/golang-migrate/migrate/v4"

var version = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if info.Main.Path == modulePath && info.Main.Version != "" {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep == nil {
			continue
		}
		if dep.Path == modulePath {
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return ""
})

// Version returns the migrate module version for use as the instrumentation
// scope version. It returns an empty string when the version cannot be
// determined (for example in tests, or when built without module info), in
// which case callers should omit the scope version entirely.
func Version() string { return version() }

// dbSystemNameYugabyteDB is not in the semantic conventions registry; the
// conventions allow a lowercase name for unlisted systems.
const dbSystemNameYugabyteDB = "yugabytedb"

// dbSystemNames maps a registered database driver scheme to the matching
// db.system.name value from the OpenTelemetry semantic conventions.
//
// Without this mapping the raw URL scheme would be reported, which disagrees
// with the value the underlying database/sql instrumentation reports for the
// very same connection (e.g. "sqlite3" vs "sqlite", "postgres" vs
// "postgresql"), producing two different db.system.name values within a single
// trace.
//
// Schemes with no registry entry are normalized to a single spelling but
// otherwise passed through, as the conventions permit for unlisted systems.
var dbSystemNames = map[string]string{
	"cassandra/v2":   semconv.DBSystemNameCassandra.Value.AsString(),
	"clickhouse":     semconv.DBSystemNameClickHouse.Value.AsString(),
	"cockroach":      semconv.DBSystemNameCockroachDB.Value.AsString(),
	"cockroachdb":    semconv.DBSystemNameCockroachDB.Value.AsString(),
	"couchbase":      semconv.DBSystemNameCouchbase.Value.AsString(),
	"couchbases":     semconv.DBSystemNameCouchbase.Value.AsString(),
	"crdb-postgres":  semconv.DBSystemNameCockroachDB.Value.AsString(),
	"firebird":       semconv.DBSystemNameFirebirdSQL.Value.AsString(),
	"firebirdsql":    semconv.DBSystemNameFirebirdSQL.Value.AsString(),
	"mongodb/v2":     semconv.DBSystemNameMongoDB.Value.AsString(),
	"mongodb/v2+srv": semconv.DBSystemNameMongoDB.Value.AsString(),
	"mysql":          semconv.DBSystemNameMySQL.Value.AsString(),
	"pgx5":           semconv.DBSystemNamePostgreSQL.Value.AsString(),
	"postgres":       semconv.DBSystemNamePostgreSQL.Value.AsString(),
	"postgresql":     semconv.DBSystemNamePostgreSQL.Value.AsString(),
	"redshift":       semconv.DBSystemNameAWSRedshift.Value.AsString(),
	"spanner":        semconv.DBSystemNameGCPSpanner.Value.AsString(),
	"sqlcipher":      semconv.DBSystemNameSQLite.Value.AsString(),
	"sqlite":         semconv.DBSystemNameSQLite.Value.AsString(),
	"sqlite3":        semconv.DBSystemNameSQLite.Value.AsString(),
	"sqlserver":      semconv.DBSystemNameMicrosoftSQLServer.Value.AsString(),
	"ysql":           dbSystemNameYugabyteDB,
	"yugabyte":       dbSystemNameYugabyteDB,
	"yugabytedb":     dbSystemNameYugabyteDB,
}

// DBSystemName maps a registered driver scheme to its db.system.name value.
// Unknown names are returned unchanged, so a caller-supplied driver name (as
// accepted by migrate.NewWithInstance) is still reported as-is.
func DBSystemName(driverName string) string {
	if v, ok := dbSystemNames[driverName]; ok {
		return v
	}
	return driverName
}
