package googlecloudstorage

import (
	"context"
	"net/url"
	"testing"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/golang-migrate/migrate/v5/source"
	st "github.com/golang-migrate/migrate/v5/source/testing"
)

func Test(t *testing.T) {
	server := fakestorage.NewServer([]fakestorage.Object{
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "staging/migrations/1_foobar.up.sql"}, Content: []byte("1 up")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "staging/migrations/1_foobar.down.sql"}, Content: []byte("1 down")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/1_foobar.up.sql"}, Content: []byte("1 up")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/1_foobar.down.sql"}, Content: []byte("1 down")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/3_foobar.up.sql"}, Content: []byte("3 up")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/4_foobar.up.sql"}, Content: []byte("4 up")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/4_foobar.down.sql"}, Content: []byte("4 down")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/5_foobar.down.sql"}, Content: []byte("5 down")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/7_foobar.up.sql"}, Content: []byte("7 up")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/7_foobar.down.sql"}, Content: []byte("7 down")},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/not-a-migration.txt"}},
		{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/0-random-stuff/whatever.txt"}},
	})
	defer server.Stop()
	driver := gcs{
		bucket:     server.Client().Bucket("some-bucket"),
		prefix:     "prod/migrations/",
		migrations: source.NewMigrations(),
	}
	err := driver.loadMigrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st.Test(t, &driver)
}

// TestOpen exercises the Open code path against a fake GCS server. The rest of
// the suite constructs the driver directly, which is how a broken client option
// in Open previously went unnoticed.
func TestOpen(t *testing.T) {
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme: "http",
		InitialObjects: []fakestorage.Object{
			{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/1_foobar.up.sql"}, Content: []byte("1 up")},
			{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "some-bucket", Name: "prod/migrations/1_foobar.down.sql"}, Content: []byte("1 down")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	u, err := url.Parse(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("STORAGE_EMULATOR_HOST", u.Host)

	ctx := context.Background()
	d, err := (&gcs{}).Open(ctx, "gcs://some-bucket/prod/migrations")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(ctx); err != nil {
			t.Error(err)
		}
	})

	version, err := d.First(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("expected version 1, got %v", version)
	}
}
