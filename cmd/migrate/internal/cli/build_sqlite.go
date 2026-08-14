//go:build sqlite

package cli

import (
	_ "github.com/golang-migrate/migrate/database/sqlite/v5"
)
