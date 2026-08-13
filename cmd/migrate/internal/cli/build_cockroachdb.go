//go:build cockroachdb

package cli

import (
	_ "github.com/golang-migrate/migrate/database/cockroachdb/v5"
)
