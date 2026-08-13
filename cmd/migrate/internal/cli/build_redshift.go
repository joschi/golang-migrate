//go:build redshift

package cli

import (
	_ "github.com/golang-migrate/migrate/v5/database/redshift"
)
