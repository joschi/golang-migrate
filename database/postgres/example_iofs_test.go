package postgres_test

import (
	"context"
	"embed"
	"log"

	"github.com/golang-migrate/migrate/v5"
	_ "github.com/golang-migrate/migrate/database/postgres/v5"
	"github.com/golang-migrate/migrate/v5/source/iofs"
)

//go:embed examples/migrations/*.sql
var fs embed.FS

func Example_iofs() {
	ctx := context.Background()
	d, err := iofs.New(fs, "examples/migrations")
	if err != nil {
		log.Fatal(err)
	}
	m, err := migrate.NewWithSourceInstance(ctx, "iofs", d, "postgres://postgres@localhost/postgres?sslmode=disable")
	if err != nil {
		log.Fatal(err)
	}
	err = m.Up(ctx)
	if err != nil {
		// ...
	}
	// ...
}
