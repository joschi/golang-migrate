//go:build cassandra

package cli

import (
	_ "github.com/golang-migrate/migrate/v5/database/cassandra"
)
