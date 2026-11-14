//go:build integration

package testdb

import (
	"context"
	"database/sql"
	"net"
	"os"
	"testing"
	"time"

	"github.com/schemigo/schemigo/clickhouse"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ClickHouseURL starts a disposable ClickHouse server and returns its default database.
// It skips without SCHEMIGO_TESTCONTAINERS=1; cleanup removes the server.
func ClickHouseURL(t *testing.T) (*sql.DB, string) {
	t.Helper()
	c := container(t, "clickhouse/clickhouse-server:25.8-alpine", "9000/tcp", map[string]string{"CLICKHOUSE_SKIP_USER_SETUP": "1"})
	host, err := c.Host(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(t.Context(), "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	dsn := "clickhouse://" + net.JoinHostPort(host, port.Port()) + "/default?dial_timeout=10s"
	db, err := clickhouse.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

// Keeper starts a disposable ZooKeeper server implementing Keeper's lock protocol.
func Keeper(t *testing.T) string {
	t.Helper()
	c := container(t, "zookeeper:3.9.3", "2181/tcp", nil)
	host, err := c.Host(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(t.Context(), "2181/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort(host, port.Port())
}
func container(t *testing.T, image, port string, env map[string]string) tc.Container {
	t.Helper()
	if os.Getenv("SCHEMIGO_TESTCONTAINERS") != "1" {
		t.Skip("set SCHEMIGO_TESTCONTAINERS=1 to allow disposable database fixtures")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	c, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{ContainerRequest: tc.ContainerRequest{Image: image, Env: env, ExposedPorts: []string{port}, WaitingFor: wait.ForListeningPort(port)}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.Terminate(ctx); err != nil {
			t.Error(err)
		}
	})
	return c
}
