# Migrating from v4 to v5

v5 splits the repository into one Go module per driver. Importing the postgres
driver no longer compiles Spanner, DuckDB, Snowflake and every other driver
into your binary.

That means **every import path changes**, and it is the reason v5 is a major
release. A nested module cannot carry its major version in the middle of its
path, so `github.com/golang-migrate/migrate/v4/database/postgres` is not a
legal module path for a separate `database/postgres` module; the suffix has
to move to the end.

## 1. Import paths

The core library keeps its shape:

```go
import "github.com/golang-migrate/migrate/v5"   // was /v4
```

Packages that stay part of the core module — `source/file`, `source/iofs`,
`source/httpfs`, `database/multistmt`, the stub and testing helpers — follow
the same pattern:

```go
import "github.com/golang-migrate/migrate/v5/source/iofs"   // was /v4/source/iofs
```

Every driver moves to its own module, with the `/v5` suffix at the end:

| v4 | v5 |
|----|----|
| `github.com/golang-migrate/migrate/v4/database/cassandra/v2` | `github.com/golang-migrate/migrate/database/cassandra/v5` |
| `github.com/golang-migrate/migrate/v4/database/clickhouse` | `github.com/golang-migrate/migrate/database/clickhouse/v5` |
| `github.com/golang-migrate/migrate/v4/database/cockroachdb` | `github.com/golang-migrate/migrate/database/cockroachdb/v5` |
| `github.com/golang-migrate/migrate/v4/database/couchbase` | `github.com/golang-migrate/migrate/database/couchbase/v5` |
| `github.com/golang-migrate/migrate/v4/database/duckdb` | `github.com/golang-migrate/migrate/database/duckdb/v5` |
| `github.com/golang-migrate/migrate/v4/database/firebird` | `github.com/golang-migrate/migrate/database/firebird/v5` |
| `github.com/golang-migrate/migrate/v4/database/mongodb/v2` | `github.com/golang-migrate/migrate/database/mongodb/v5` |
| `github.com/golang-migrate/migrate/v4/database/mysql` | `github.com/golang-migrate/migrate/database/mysql/v5` |
| `github.com/golang-migrate/migrate/v4/database/neo4j` | `github.com/golang-migrate/migrate/database/neo4j/v5` |
| `github.com/golang-migrate/migrate/v4/database/pgx/v5` | `github.com/golang-migrate/migrate/database/pgx5/v5` |
| `github.com/golang-migrate/migrate/v4/database/postgres` | `github.com/golang-migrate/migrate/database/postgres/v5` |
| `github.com/golang-migrate/migrate/v4/database/ql` | `github.com/golang-migrate/migrate/database/ql/v5` |
| `github.com/golang-migrate/migrate/v4/database/redshift` | `github.com/golang-migrate/migrate/database/redshift/v5` |
| `github.com/golang-migrate/migrate/v4/database/rqlite` | `github.com/golang-migrate/migrate/database/rqlite/v5` |
| `github.com/golang-migrate/migrate/v4/database/snowflake` | `github.com/golang-migrate/migrate/database/snowflake/v5` |
| `github.com/golang-migrate/migrate/v4/database/spanner` | `github.com/golang-migrate/migrate/database/spanner/v5` |
| `github.com/golang-migrate/migrate/v4/database/sqlcipher` | `github.com/golang-migrate/migrate/database/sqlcipher/v5` |
| `github.com/golang-migrate/migrate/v4/database/sqlite` | `github.com/golang-migrate/migrate/database/sqlite/v5` |
| `github.com/golang-migrate/migrate/v4/database/sqlite3` | `github.com/golang-migrate/migrate/database/sqlite3/v5` |
| `github.com/golang-migrate/migrate/v4/database/sqlserver` | `github.com/golang-migrate/migrate/database/sqlserver/v5` |
| `github.com/golang-migrate/migrate/v4/database/yugabytedb` | `github.com/golang-migrate/migrate/database/yugabytedb/v5` |
| `github.com/golang-migrate/migrate/v4/source/aws_s3` | `github.com/golang-migrate/migrate/source/aws_s3/v5` |
| `github.com/golang-migrate/migrate/v4/source/bitbucket` | `github.com/golang-migrate/migrate/source/bitbucket/v5` |
| `github.com/golang-migrate/migrate/v4/source/gitea` | `github.com/golang-migrate/migrate/source/gitea/v5` |
| `github.com/golang-migrate/migrate/v4/source/github` | `github.com/golang-migrate/migrate/source/github/v5` |
| `github.com/golang-migrate/migrate/v4/source/github_ee` | `github.com/golang-migrate/migrate/source/github_ee/v5` |
| `github.com/golang-migrate/migrate/v4/source/gitlab` | `github.com/golang-migrate/migrate/source/gitlab/v5` |
| `github.com/golang-migrate/migrate/v4/source/go_bindata` | `github.com/golang-migrate/migrate/source/go_bindata/v5` |
| `github.com/golang-migrate/migrate/v4/source/godoc_vfs` | `github.com/golang-migrate/migrate/source/godoc_vfs/v5` |
| `github.com/golang-migrate/migrate/v4/source/google_cloud_storage` | `github.com/golang-migrate/migrate/source/google_cloud_storage/v5` |
| `github.com/golang-migrate/migrate/v4/source/pkger` | `github.com/golang-migrate/migrate/source/pkger/v5` |

Each driver is now required separately:

```
go get github.com/golang-migrate/migrate/v5
go get github.com/golang-migrate/migrate/database/postgres/v5
```

The CLI is a module too:

```
go install github.com/golang-migrate/migrate/cmd/migrate/v5@latest
```

## 2. Renamed drivers

Three drivers carried a version suffix in their directory. A nested module in
`database/foo/v2` would be pinned to major v2 forever and could not follow the
repository-wide version, so the suffixes are gone:

| | v4 | v5 |
|-|----|----|
| Cassandra package | `v2.WithInstance(...)` | `cassandra.WithInstance(...)` |
| MongoDB package | `v2.WithInstance(...)` | `mongodb.WithInstance(...)` |
| PGX v5 package | `pgx.WithInstance(...)` | unchanged |

## 3. Database URLs (CLI users)

The DSN schemes lost their suffixes as well. **This breaks existing commands,
scripts and CI pipelines**, not just Go code:

| v4 | v5 |
|----|----|
| `cassandra/v2://...` | `cassandra://...` |
| `mongodb/v2://...` | `mongodb://...` |
| `mongodb/v2+srv://...` | `mongodb+srv://...` |
| `pgx5://...` | unchanged |

## 4. Build tags

If you build a custom CLI with `-tags`:

| v4 | v5 |
|----|----|
| `cassandra_v2` | `cassandra` |
| `mongodb_v2` | `mongodb` |
| `pgx5` | unchanged |

## 5. Removed

- The deprecated `cli` package. Use `cmd/migrate`.
- `internal/dbotel` is now `database/dbotel`. It is exported because drivers
  live in their own modules and cannot import an internal package of the core
  module; it is a helper for driver implementations, not an end-user API.
