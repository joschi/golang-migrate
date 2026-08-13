//go:build postgres

package cli

import (
	_ "github.com/golang-migrate/migrate/database/postgres/v5"
)
