//go:build spanner

package cli

import (
	_ "github.com/golang-migrate/migrate/database/spanner/v5"
)
