//go:build sqlcipher

package cli

import (
	_ "github.com/golang-migrate/migrate/v5/database/sqlcipher"
)
