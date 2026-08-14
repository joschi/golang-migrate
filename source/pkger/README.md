# pkger
```go
package main

import (
	"errors"
	"log"

	"github.com/golang-migrate/migrate/v5"
	"github.com/markbates/pkger"

	_ "github.com/golang-migrate/migrate/database/postgres/v5"
	_ "github.com/golang-migrate/migrate/source/pkger/v5"
	_ "github.com/lib/pq"
)

func main() {
	pkger.Include("/module/path/to/migrations")
	m, err := migrate.New("pkger:///module/path/to/migrations", "postgres://postgres@localhost/postgres?sslmode=disable")
	if err != nil {
		log.Fatalln(err)
	}
	if err := m.Up(); errors.Is(err, migrate.ErrNoChange) {
		log.Println(err)
	} else if err != nil {
		log.Fatalln(err)
	}
}
```
