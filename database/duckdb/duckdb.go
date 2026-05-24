package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	nurl "net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"

	_ "github.com/duckdb/duckdb-go/v2"
)

func init() {
	database.Register("duckdb", &DuckDB{})
}

var DefaultMigrationsTable = "schema_migrations"

var (
	ErrNilConfig = errors.New("no config")
)

type Config struct {
	MigrationsTable string
	NoTxWrap        bool
}

type DuckDB struct {
	db       *sql.DB
	isLocked atomic.Bool
	config   *Config
}

func (d *DuckDB) Open(ctx context.Context, url string) (database.Driver, error) {
	purl, err := nurl.Parse(url)
	if err != nil {
		return nil, fmt.Errorf("parsing url: %w", err)
	}
	dbfile := strings.Replace(migrate.FilterCustomQuery(purl).String(), "duckdb://", "", 1)
	db, err := sql.Open("duckdb", dbfile)
	if err != nil {
		return nil, fmt.Errorf("opening '%s': %w", dbfile, err)
	}

	qv := purl.Query()
	migrationsTable := qv.Get("x-migrations-table")
	if len(migrationsTable) == 0 {
		migrationsTable = DefaultMigrationsTable
	}

	noTxWrap := false
	if v := qv.Get("x-no-tx-wrap"); v != "" {
		noTxWrap, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("x-no-tx-wrap: %s", err)
		}
	}

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("pinging: %w", err)
	}
	cfg := &Config{
		MigrationsTable: migrationsTable,
		NoTxWrap:        noTxWrap,
	}
	return WithInstance(ctx, db, cfg)
}

func (d *DuckDB) Close(ctx context.Context) error {
	return d.db.Close()
}

func (d *DuckDB) Lock(ctx context.Context) error {
	if !d.isLocked.CompareAndSwap(false, true) {
		return database.ErrLocked
	}
	return nil
}

func (d *DuckDB) Unlock(ctx context.Context) error {
	if !d.isLocked.CompareAndSwap(true, false) {
		return database.ErrNotLocked
	}
	return nil
}

func (d *DuckDB) Drop(ctx context.Context) error {
	tablesQuery := `SELECT schema_name, table_name FROM duckdb_tables()`
	tables, err := d.db.QueryContext(ctx, tablesQuery)
	if err != nil {
		return &database.Error{OrigErr: err, Query: []byte(tablesQuery)}
	}
	defer func() {
		if errClose := tables.Close(); errClose != nil {
			err = errors.Join(err, errClose)
		}
	}()

	tableNames := []string{}
	for tables.Next() {
		var (
			schemaName string
			tableName  string
		)

		if err := tables.Scan(&schemaName, &tableName); err != nil {
			return &database.Error{OrigErr: err, Err: "scanning schema and table name"}
		}

		if len(schemaName) > 0 {
			tableNames = append(tableNames, fmt.Sprintf("%s.%s", schemaName, tableName))
		} else {
			tableNames = append(tableNames, tableName)
		}
	}
	if err := tables.Err(); err != nil {
		return &database.Error{OrigErr: err, Query: []byte(tablesQuery), Err: "err in rows after scanning"}
	}

	for _, t := range tableNames {
		dropQuery := fmt.Sprintf("DROP TABLE %s", t)
		if _, err := d.db.ExecContext(ctx, dropQuery); err != nil {
			return &database.Error{OrigErr: err, Query: []byte(dropQuery)}
		}
	}

	return nil

}

func (d *DuckDB) SetVersion(ctx context.Context, version int, dirty bool) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return &database.Error{OrigErr: err, Err: "transaction start failed"}
	}

	query := "DELETE FROM " + d.config.MigrationsTable
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return &database.Error{OrigErr: err, Query: []byte(query)}
	}

	// Also re-write the schema version for nil dirty versions to prevent
	// empty schema version for failed down migration on the first migration
	// See: https://github.com/golang-migrate/migrate/issues/330
	//
	// NOTE: Copied from sqlite implementation, unsure if this is necessary for
	// duckdb
	if version >= 0 || (version == database.NilVersion && dirty) {
		query := fmt.Sprintf(`INSERT INTO %s (version, dirty) VALUES (?, ?)`, d.config.MigrationsTable)
		if _, err := tx.ExecContext(ctx, query, version, dirty); err != nil {
			if errRollback := tx.Rollback(); errRollback != nil {
				err = errors.Join(err, errRollback)
			}
			return &database.Error{OrigErr: err, Query: []byte(query)}
		}
	}

	if err := tx.Commit(); err != nil {
		return &database.Error{OrigErr: err, Err: "transaction commit failed"}
	}

	return nil
}

func (m *DuckDB) Version(ctx context.Context) (version int, dirty bool, err error) {
	query := "SELECT version, dirty FROM " + m.config.MigrationsTable + " LIMIT 1"
	err = m.db.QueryRowContext(ctx, query).Scan(&version, &dirty)
	if err != nil {
		return database.NilVersion, false, nil
	}
	return version, dirty, nil
}

func (d *DuckDB) Run(ctx context.Context, migration io.Reader) error {
	migr, err := io.ReadAll(migration)
	if err != nil {
		return fmt.Errorf("reading migration: %w", err)
	}
	query := string(migr[:])

	if d.config.NoTxWrap {
		if _, err := d.db.ExecContext(ctx, query); err != nil {
			return &database.Error{OrigErr: err, Query: []byte(query)}
		}
		return nil
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return &database.Error{OrigErr: err, Err: "transaction start failed"}
	}
	if _, err := tx.ExecContext(ctx, query); err != nil {
		if errRollback := tx.Rollback(); errRollback != nil {
			err = errors.Join(err, errRollback)
		}
		return &database.Error{OrigErr: err, Query: []byte(query)}
	}
	if err := tx.Commit(); err != nil {
		return &database.Error{OrigErr: err, Err: "transaction commit failed"}
	}
	return nil
}

// ensureVersionTable checks if versions table exists and, if not, creates it.
// Note that this function locks the database, which deviates from the usual
// convention of "caller locks" in the DuckDB type.
func (d *DuckDB) ensureVersionTable(ctx context.Context) (err error) {
	if err = d.Lock(ctx); err != nil {
		return err
	}

	defer func() {
		if e := d.Unlock(ctx); e != nil {
			err = errors.Join(err, e)
		}
	}()

	query := fmt.Sprintf(`
	CREATE TABLE IF NOT EXISTS %s (version BIGINT, dirty BOOLEAN);
	CREATE UNIQUE INDEX IF NOT EXISTS version_unique ON %s (version);
`, d.config.MigrationsTable, d.config.MigrationsTable)

	if _, err := d.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("creating version table via '%s': %w", query, err)
	}
	return nil
}

func WithInstance(ctx context.Context, instance *sql.DB, config *Config) (database.Driver, error) {
	if config == nil {
		return nil, ErrNilConfig
	}

	if err := instance.PingContext(ctx); err != nil {
		return nil, err
	}

	if len(config.MigrationsTable) == 0 {
		config.MigrationsTable = DefaultMigrationsTable
	}

	mx := &DuckDB{
		db:     instance,
		config: config,
	}
	if err := mx.ensureVersionTable(ctx); err != nil {
		return nil, err
	}
	return mx, nil
}
