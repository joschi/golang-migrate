//go:build gitea

package cli

import (
	_ "github.com/golang-migrate/migrate/source/gitea/v5"
)
