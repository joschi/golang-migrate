//go:build sqlite3

package cli

import (
	_ "github.com/golang-migrate/migrate/database/sqlite3/v5"
)
