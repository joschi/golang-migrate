//go:build sqlite

package cli

import (
	_ "github.com/golang-migrate/migrate/v5/database/sqlite"
)
