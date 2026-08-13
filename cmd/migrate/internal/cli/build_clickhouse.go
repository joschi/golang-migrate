//go:build clickhouse

package cli

import (
	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/golang-migrate/migrate/v5/database/clickhouse"
)
