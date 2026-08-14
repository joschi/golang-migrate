package iofs_test

import (
	"embed"
	"testing"

	"github.com/golang-migrate/migrate/v5/source/iofs"
	st "github.com/golang-migrate/migrate/v5/source/testing"
)

//go:embed testdata/migrations/*.sql
var fs embed.FS

func Test(t *testing.T) {
	d, err := iofs.New(fs, "testdata/migrations")
	if err != nil {
		t.Fatal(err)
	}

	st.Test(t, d)
}
