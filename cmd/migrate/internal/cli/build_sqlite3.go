//go:build sqlite3

package cli

import (
	_ "github.com/golang-migrate/migrate/v5/database/sqlite3"
)
