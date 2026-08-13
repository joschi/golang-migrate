//go:build sqlcipher

package cli

import (
	_ "github.com/golang-migrate/migrate/database/sqlcipher/v5"
)
