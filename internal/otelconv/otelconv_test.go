package otelconv

import "testing"

// TestDBSystemName covers the schemes where the registered driver name differs
// from the semantic convention value. Reporting the raw scheme made the driver
// wrapper disagree with the database/sql instrumentation inside the same trace.
func TestDBSystemName(t *testing.T) {
	tests := map[string]string{
		// Mapped to a registry value.
		"postgres":       "postgresql",
		"postgresql":     "postgresql",
		"pgx5":           "postgresql",
		"sqlite3":        "sqlite",
		"sqlite":         "sqlite",
		"sqlcipher":      "sqlite",
		"cockroach":      "cockroachdb",
		"cockroachdb":    "cockroachdb",
		"crdb-postgres":  "cockroachdb",
		"redshift":       "aws.redshift",
		"sqlserver":      "microsoft.sql_server",
		"spanner":        "gcp.spanner",
		"mongodb/v2":     "mongodb",
		"mongodb/v2+srv": "mongodb",
		"cassandra/v2":   "cassandra",
		"couchbase":      "couchbase",
		"couchbases":     "couchbase",
		"firebird":       "firebirdsql",
		"firebirdsql":    "firebirdsql",
		"mysql":          "mysql",
		"clickhouse":     "clickhouse",

		// Normalised, but absent from the registry.
		"yugabyte":   "yugabytedb",
		"ysql":       "yugabytedb",
		"yugabytedb": "yugabytedb",

		// Not in the registry: passed through unchanged.
		"duckdb":    "duckdb",
		"ql":        "ql",
		"rqlite":    "rqlite",
		"snowflake": "snowflake",
		"neo4j":     "neo4j",

		// Caller-supplied names (NewWithInstance) are passed through.
		"my-custom-db": "my-custom-db",
		"":             "",
	}

	for in, want := range tests {
		if got := DBSystemName(in); got != want {
			t.Errorf("DBSystemName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDBSystemNameCoversRegisteredSchemes is a reminder to extend the mapping
// when a driver is added: every scheme migrate registers should have a
// deliberate entry or be a known pass-through.
func TestDBSystemNameCoversRegisteredSchemes(t *testing.T) {
	// Schemes with no semantic convention value, intentionally passed through.
	passThrough := map[string]bool{
		"duckdb": true, "ql": true, "rqlite": true, "snowflake": true,
		"neo4j": true, "stub": true,
	}
	// Registered schemes, kept in sync with the database/* drivers by hand
	// because importing them here would pull in every database dependency.
	registered := []string{
		"cassandra/v2", "clickhouse", "cockroach", "cockroachdb", "couchbase",
		"couchbases", "crdb-postgres", "duckdb", "firebird", "firebirdsql",
		"mongodb/v2", "mongodb/v2+srv", "mysql", "neo4j", "pgx5", "postgres",
		"postgresql", "ql", "redshift", "rqlite", "snowflake", "spanner",
		"sqlcipher", "sqlite", "sqlite3", "sqlserver", "stub", "ysql",
		"yugabyte", "yugabytedb",
	}

	for _, scheme := range registered {
		_, mapped := dbSystemNames[scheme]
		if !mapped && !passThrough[scheme] {
			t.Errorf("scheme %q is neither mapped nor a known pass-through; "+
				"add it to dbSystemNames or to passThrough in this test", scheme)
		}
	}
}
