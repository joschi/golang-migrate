[![GitHub Workflow Status (branch)](https://img.shields.io/github/actions/workflow/status/golang-migrate/migrate/ci.yaml?branch=master)](https://github.com/golang-migrate/migrate/actions/workflows/ci.yaml?query=branch%3Amaster)
[![GoDoc](https://pkg.go.dev/badge/github.com/golang-migrate/migrate)](https://pkg.go.dev/github.com/golang-migrate/migrate/v5)
[![Coverage Status](https://img.shields.io/coveralls/github/golang-migrate/migrate/master.svg)](https://coveralls.io/github/golang-migrate/migrate?branch=master)
[![packagecloud.io](https://img.shields.io/badge/deb-packagecloud.io-844fec.svg)](https://packagecloud.io/golang-migrate/migrate?filter=debs)
[![Docker Pulls](https://img.shields.io/docker/pulls/migrate/migrate.svg)](https://hub.docker.com/r/migrate/migrate/)
![Supported Go Versions](https://img.shields.io/badge/Go-1.26-lightgrey.svg)
[![GitHub Release](https://img.shields.io/github/release/golang-migrate/migrate.svg)](https://github.com/golang-migrate/migrate/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/golang-migrate/migrate/v5)](https://goreportcard.com/report/github.com/golang-migrate/migrate/v5)

# migrate

__Database migrations written in Go. Use as [CLI](#cli-usage) or import as [library](#use-in-your-go-project).__

* Migrate reads migrations from [sources](#migration-sources)
   and applies them in correct order to a [database](#databases).
* Drivers are "dumb", migrate glues everything together and makes sure the logic is bulletproof.
   (Keeps the drivers lightweight, too.)
* Database drivers don't assume things or try to correct user input. When in doubt, fail.

Forked from [mattes/migrate](https://github.com/mattes/migrate)

## Databases

Database drivers run migrations. [Add a new database?](database/driver.go)

* [PostgreSQL](database/postgres)
* [PGX v5](database/pgx5)
* [Redshift](database/redshift)
* [Ql](database/ql)
* [Cassandra / ScyllaDB](database/cassandra)
* [SQLite](database/sqlite)
* [SQLite3](database/sqlite3) ([todo #165](https://github.com/mattes/migrate/issues/165))
* [SQLCipher](database/sqlcipher)
* [MySQL / MariaDB](database/mysql)
* [Neo4j](database/neo4j)
* [MongoDB](database/mongodb)
* [Google Cloud Spanner](database/spanner)
* [CockroachDB](database/cockroachdb)
* [YugabyteDB](database/yugabytedb)
* [ClickHouse](database/clickhouse)
* [DuckDB](database/duckdb)
* [Firebird](database/firebird)
* [MS SQL Server](database/sqlserver)
* [rqlite](database/rqlite)

### Database URLs

Database connection strings are specified via URLs. The URL format is driver dependent but generally has the form: `dbdriver://username:password@host:port/dbname?param1=true&param2=false`

Any [reserved URL characters](https://en.wikipedia.org/wiki/Percent-encoding#Percent-encoding_reserved_characters) need to be escaped. Note, the `%` character also [needs to be escaped](https://en.wikipedia.org/wiki/Percent-encoding#Percent-encoding_the_percent_character)

Explicitly, the following characters need to be escaped:
`!`, `#`, `$`, `%`, `&`, `'`, `(`, `)`, `*`, `+`, `,`, `/`, `:`, `;`, `=`, `?`, `@`, `[`, `]`

It's easiest to always run the URL parts of your DB connection URL (e.g. username, password, etc) through an URL encoder. See the example Python snippets below:

```bash
$ python3 -c 'import urllib.parse; print(urllib.parse.quote(input("String to encode: "), ""))'
String to encode: FAKEpassword!#$%&'()*+,/:;=?@[]
FAKEpassword%21%23%24%25%26%27%28%29%2A%2B%2C%2F%3A%3B%3D%3F%40%5B%5D
$ python2 -c 'import urllib; print urllib.quote(raw_input("String to encode: "), "")'
String to encode: FAKEpassword!#$%&'()*+,/:;=?@[]
FAKEpassword%21%23%24%25%26%27%28%29%2A%2B%2C%2F%3A%3B%3D%3F%40%5B%5D
$
```

## Migration Sources

Source drivers read migrations from local or remote sources. [Add a new source?](source/driver.go)

* [Filesystem](source/file) - read from filesystem
* [io/fs](source/iofs) - read from a Go [io/fs](https://pkg.go.dev/io/fs#FS)
* [Go-Bindata](source/go_bindata) - read from embedded binary data ([jteeuwen/go-bindata](https://github.com/jteeuwen/go-bindata))
* [pkger](source/pkger) - read from embedded binary data ([markbates/pkger](https://github.com/markbates/pkger))
* [GitHub](source/github) - read from remote GitHub repositories
* [GitHub Enterprise](source/github_ee) - read from remote GitHub Enterprise repositories
* [Bitbucket](source/bitbucket) - read from remote Bitbucket repositories
* [Gitlab](source/gitlab) - read from remote Gitlab repositories
* [Gitea](source/gitea) - read from remote Gitea repositories
* [AWS S3](source/aws_s3) - read from Amazon Web Services S3
* [Google Cloud Storage](source/google_cloud_storage) - read from Google Cloud Platform Storage

## CLI usage

* Simple wrapper around this library.
* Handles ctrl+c (SIGINT) gracefully.
* No config search paths, no config files, no magic ENV var injections.

[CLI Documentation](cmd/migrate) (includes CLI install instructions)

### Basic usage

```bash
$ migrate -source file://path/to/migrations -database postgres://localhost:5432/database up 2
```

### Docker usage

```bash
$ docker run -v {{ migration dir }}:/migrations --network host migrate/migrate
    -path=/migrations/ -database postgres://localhost:5432/database up 2
```

## Use in your Go project

* API is stable and frozen for this release (v3 & v4).
* Uses [Go modules](https://golang.org/cmd/go/#hdr-Modules__module_versions__and_more) to manage dependencies.
* To help prevent database corruptions, it supports graceful stops via `GracefulStop chan bool`.
* Bring your own logger.
* Uses `io.Reader` streams internally for low memory overhead.
* Thread-safe and no goroutine leaks.
* Emits [OpenTelemetry](https://opentelemetry.io) traces and metrics automatically when a global provider is configured by the host application (see [OpenTelemetry](#opentelemetry) below).

__[Go Documentation](https://pkg.go.dev/github.com/golang-migrate/migrate/v5)__

```go
import (
    "github.com/golang-migrate/migrate/v5"
    _ "github.com/golang-migrate/migrate/v5/database/postgres"
    _ "github.com/golang-migrate/migrate/v5/source/github"
)

func main() {
    m, err := migrate.New(
        "github://mattes:personal-access-token@mattes/migrate_test",
        "postgres://localhost:5432/database?sslmode=enable")
    m.Steps(2)
}
```

Want to use an existing database client?

```go
import (
    "database/sql"
    _ "github.com/lib/pq"
    "github.com/golang-migrate/migrate/v5"
    "github.com/golang-migrate/migrate/v5/database/postgres"
    _ "github.com/golang-migrate/migrate/v5/source/file"
)

func main() {
    db, err := sql.Open("postgres", "postgres://localhost:5432/database?sslmode=enable")
    driver, err := postgres.WithInstance(db, &postgres.Config{})
    m, err := migrate.NewWithDatabaseInstance(
        "file:///migrations",
        "postgres", driver)
    m.Up() // or m.Steps(2) if you want to explicitly set the number of migrations to run
}
```

## Getting started

Go to [getting started](GETTING_STARTED.md)

## OpenTelemetry

migrate emits [OpenTelemetry](https://opentelemetry.io) traces and metrics through the OTel **API** only — the library does **not** initialize an SDK, configure exporters, or set up resources. This means:

- **Zero overhead** when no OTel SDK is configured (global no-op providers are used).
- **Automatic telemetry** when the host application configures OTel global providers (e.g. via `go.opentelemetry.io/otel/sdk`), or when a provider is passed explicitly with `migrate.WithTracerProvider` / `migrate.WithMeterProvider`.

The `migrate` CLI is different: it *can* set up an SDK, but only when asked. See [CLI](#cli) below.

### Signals

**Traces** — one parent span per public operation (`migrate.up`, `migrate.down`, `migrate.migrate`, etc.), one child span per migration execution (`migrate.run_migration`), and driver-level spans for every database and source operation.

#### Operation spans (SpanKind: INTERNAL)

| Span name | Emitted by |
|-----------|-----------|
| `migrate.open` | `New` / `NewWithDatabaseInstance` / `NewWithSourceInstance` |
| `migrate.up` | `Up` / `Steps` (positive) |
| `migrate.down` | `Down` / `Steps` (negative) |
| `migrate.migrate` | `Migrate` |
| `migrate.force` | `Force` |
| `migrate.drop` | `Drop` |
| `migrate.version` | `Version` |
| `migrate.close` | `Close` |
| `migrate.run_migration` | Per-migration child of the above |
| `migrate.buffer_migration` | Per-migration read of the body from the source |

`migrate.open` exists so that work a driver performs while opening — creating the
migrations table, listing the source — is attached to a span instead of starting
its own trace. `migrate.buffer_migration` covers the actual read of a migration
body, which happens after `source.read_up` returns the reader; for remote
sources this is where the download time is.

#### Database driver spans (SpanKind: CLIENT)

Span names follow the database semantic conventions: `{db.operation.name} {target}`,
falling back to `{db.operation.name}` when there is no target. The target is the
migrations table, so with the default table the names are as shown; a driver
configured with `x-migrations-table=custom` reports `set_version custom`.

| Span name | Method | Key attributes |
|-----------|--------|----------------|
| `lock` | Lock | `db.system.name`, `db.operation.name` |
| `unlock` | Unlock | `db.system.name`, `db.operation.name` |
| `run` | Run | `db.system.name`, `db.operation.name` |
| `set_version schema_migrations` | SetVersion | `db.system.name`, `db.operation.name`, `db.collection.name`, `migrate.version`, `migrate.dirty` |
| `get_version schema_migrations` | Version | `db.system.name`, `db.operation.name`, `db.collection.name` |
| `drop` | Drop | `db.system.name`, `db.operation.name` |

Only the two version operations carry `db.collection.name`, and only they are
named after the migrations table. `Run` executes arbitrary migration SQL, `Drop`
targets the whole database, and `Lock`/`Unlock` are usually advisory locks, so
naming those after the migrations table would misreport what they touch.

A driver supplies its table by implementing the optional
`database.MigrationsTabler` interface. All bundled drivers do, except the `stub`
test driver; `neo4j` reports its node label, which is its equivalent of a
collection. Drivers that do not implement it still get spans, named after the
operation alone.

#### Source driver spans (SpanKind: INTERNAL)

| Span name | Method | Key attributes |
|-----------|--------|----------------|
| `source.read_up` | ReadUp | `migrate.source`, `migrate.version` |
| `source.read_down` | ReadDown | `migrate.source`, `migrate.version` |

**Metrics:**

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `migrate.migrations.applied` | Counter | `{migration}` | Number of migrations successfully applied |
| `migrate.migrations.failed` | Counter | `{migration}` | Number of migrations that failed to apply |
| `migrate.migration.run.duration` | Histogram | `s` | Execution duration of a single migration |
| `migrate.migration.buffer.duration` | Histogram | `s` | Duration of reading a migration body from the source |
| `migrate.migration.source.read` | Counter | `By` | Bytes read from the migration source |
| `migrate.lock.duration` | Histogram | `s` | Duration of database lock acquisition |
| `migrate.lock.failures` | Counter | `{failure}` | Failed lock acquisitions, including timeouts |

**Common attributes:** `db.system.name`, `db.operation.name`, `db.collection.name`, `migrate.source`, `migrate.direction`, `migrate.version`, `migrate.target_version`, `migrate.identifier`, `error.type`.

`migrate.migrations.failed` also carries `migrate.stage` (`run`,
`set_version_dirty`, `set_version_clean`) so a broken migration can be told
apart from failed version bookkeeping. `migrate.lock.failures` carries
`error.type` (`timeout`, `locked`, `graceful_stop`, `_OTHER`).

A stop signalled on `GracefulStop` while migrate is still waiting for the
database lock now cancels that attempt instead of waiting out `LockTimeout`.
Nothing is migrated, and the stop stays latched for the rest of the instance's
life.

`db.system.name` uses the semantic convention value for the database, not the
URL scheme — a `sqlite3://` URL reports `sqlite`, `postgres://` reports
`postgresql`. This keeps it consistent with the value the underlying
`database/sql` instrumentation reports for the same connection.

### Migration contents are not recorded

Migration bodies frequently contain data — seed rows, backfills, tenant
identifiers — so they are kept out of telemetry:

- The `database/sql` based drivers disable `db.query.text`, so statements are not
  attached to spans.
- Errors are redacted before being recorded: neither the span status description
  nor the `exception` event includes the failing query. Errors *returned to the
  caller* are unchanged.

### Example

```go
import (
    "context"

    "github.com/golang-migrate/migrate/v5"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    // ... your exporter of choice
)

ctx := context.Background()
tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(yourExporter))
defer tp.Shutdown(ctx)

// Pass the provider explicitly...
m, _ := migrate.New(ctx, "file:///migrations", "postgres://...",
    migrate.WithTracerProvider(tp))
m.Up(ctx)

// ...or set it globally with otel.SetTracerProvider(tp) and omit the option.
```

### CLI

The `migrate` binary emits **nothing** unless asked, so existing invocations are
unaffected and no connection to a collector is attempted. Traces and metrics are
enabled **independently**: a signal is exported when its own exporter variable is
set, or when an OTLP endpoint is configured.

- `OTEL_TRACES_EXPORTER` / `OTEL_METRICS_EXPORTER` (e.g. `otlp`, `console`, `none`)
- `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` / `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`

Enabling one signal never starts an exporter for the other, so
`OTEL_TRACES_EXPORTER=console` prints spans without any metrics exporter trying
to reach a collector. Setting `OTEL_SDK_DISABLED=true` disables everything.

All other standard `OTEL_*` variables (`OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`,
`OTEL_EXPORTER_OTLP_PROTOCOL`, ...) are honoured. Telemetry is flushed before
exit, including on error paths.

```bash
# send traces to a local collector
OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
  migrate -path=./migrations -database "postgres://..." up

# print spans to stdout (metrics stay off — each signal is enabled separately)
OTEL_TRACES_EXPORTER=console \
  migrate -path=./migrations -database "postgres://..." up
```

### Spanner

The Spanner client publishes metrics of its own (session and channel pool,
per-operation). They are gated behind `spanner.EnableOpenTelemetryMetrics()`, a
process-wide switch, so the driver leaves it to the host application to call —
after which those metrics flow to the global `MeterProvider`.

### Not instrumented

These database drivers are covered by the generic `db.*` spans, but their
clients emit no telemetry of their own: `cassandra`, `couchbase`,
`mongodb`, `neo4j`. Contributions welcome.

## Tutorials

* [CockroachDB](database/cockroachdb/TUTORIAL.md)
* [PostgreSQL](database/postgres/TUTORIAL.md)

(more tutorials to come)

## Migration files

Each migration has an up and down migration. [Why?](FAQ.md#why-two-separate-files-up-and-down-for-a-migration)

```bash
1481574547_create_users_table.up.sql
1481574547_create_users_table.down.sql
```

[Best practices: How to write migrations.](MIGRATIONS.md)

## Coming from another db migration tool?

Check out [migradaptor](https://github.com/musinit/migradaptor/).
*Note: migradaptor is not affiliated or supported by this project*

## Versions

Version | Supported? | Import | Notes
--------|------------|--------|------
**master** | :white_check_mark: | `import "github.com/golang-migrate/migrate/v5"` | New features and bug fixes arrive here first |
**v4** | :white_check_mark: | `import "github.com/golang-migrate/migrate/v5"` | Used for stable releases |
**v3** | :x: | `import "github.com/golang-migrate/migrate"` (with package manager) or `import "gopkg.in/golang-migrate/migrate.v3"` (not recommended) | **DO NOT USE** - No longer supported |

## Development and Contributing

Yes, please! [`Makefile`](Makefile) is your friend,
read the [development guide](CONTRIBUTING.md).

Also have a look at the [FAQ](FAQ.md).

---

Looking for alternatives? [https://awesome-go.com/#database](https://awesome-go.com/#database).
