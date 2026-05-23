package v2

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/dhui/dktest"
	"github.com/golang-migrate/migrate/v4"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	dt "github.com/golang-migrate/migrate/v4/database/testing"
	"github.com/golang-migrate/migrate/v4/dktesting"

	_ "github.com/golang-migrate/migrate/v4/source/file"
)

var (
	opts = dktest.Options{PortRequired: true, ReadyFunc: isReady}
	// Supported versions:
	// - https://cassandra.apache.org/_/download.html
	// - https://docs.scylladb.com/stable/versioning/version-support.html
	specs = []dktesting.ContainerSpec{
		{ImageName: "cassandra:4.0", Options: opts},
		{ImageName: "cassandra:4.1", Options: opts},
		{ImageName: "cassandra:5.0", Options: opts},
		{ImageName: "scylladb/scylla:2025.1", Options: opts},
		{ImageName: "scylladb/scylla:2025.4", Options: opts},
		{ImageName: "scylladb/scylla:2026.1", Options: opts},
	}
)

func isReady(ctx context.Context, c dktest.ContainerInfo) bool {
	// Cassandra exposes 5 ports (7000, 7001, 7199, 9042 & 9160)
	// We only need the port bound to 9042
	ip, portStr, err := c.Port(9042)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return false
	}

	cluster := gocql.NewCluster(ip)
	cluster.Port = port
	cluster.Consistency = gocql.All
	p, err := cluster.CreateSession()
	if err != nil {
		return false
	}
	defer p.Close()
	// Create keyspace for tests
	if err = p.Query("CREATE KEYSPACE testks WITH REPLICATION = {'class': 'SimpleStrategy', 'replication_factor':1}").Exec(); err != nil {
		return false
	}
	return true
}

func Test(t *testing.T) {
	t.Run("test", test)
	t.Run("testMigrate", testMigrate)

	t.Cleanup(func() {
		for _, spec := range specs {
			t.Log("Cleaning up ", spec.ImageName)
			if err := spec.Cleanup(); err != nil {
				t.Error("Error removing ", spec.ImageName, "error:", err)
			}
		}
	})
}

func test(t *testing.T) {
	dktesting.ParallelTest(t, specs, func(t *testing.T, c dktest.ContainerInfo) {
		ip, port, err := c.Port(9042)
		if err != nil {
			t.Fatal("Unable to get mapped port:", err)
		}
		addr := fmt.Sprintf("cassandra://%v:%v/testks", ip, port)
		p := &Cassandra{}
		d, err := p.Open(context.Background(), addr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := d.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		dt.Test(t, d, []byte("SELECT table_name from system_schema.tables"))
	})
}

func testMigrate(t *testing.T) {
	dktesting.ParallelTest(t, specs, func(t *testing.T, c dktest.ContainerInfo) {
		ip, port, err := c.Port(9042)
		if err != nil {
			t.Fatal("Unable to get mapped port:", err)
		}
		addr := fmt.Sprintf("cassandra://%v:%v/testks", ip, port)
		p := &Cassandra{}
		d, err := p.Open(context.Background(), addr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := d.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()

		m, err := migrate.NewWithDatabaseInstance(context.Background(), "file://./examples/migrations", "testks", d)
		if err != nil {
			t.Fatal(err)
		}
		dt.TestMigrate(t, m)
	})
}
