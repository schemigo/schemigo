//go:build integration

package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/schemigo/schemigo/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var mysqlServer struct {
	once      sync.Once
	container *testcontainers.DockerContainer
	admin     *sql.DB
	host      string
	port      string
	err       error
}

func startMySQL() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	mysqlServer.container, mysqlServer.err = testcontainers.Run(ctx, "mysql:8.4",
		testcontainers.WithEnv(map[string]string{"MYSQL_ROOT_PASSWORD": "schemigo", "MYSQL_DATABASE": "schemigo"}),
		testcontainers.WithExposedPorts("3306/tcp"),
		testcontainers.WithWaitStrategy(wait.ForLog("ready for connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	if mysqlServer.err != nil {
		return
	}
	mysqlServer.host, mysqlServer.err = mysqlServer.container.Host(ctx)
	if mysqlServer.err != nil {
		return
	}
	port, err := mysqlServer.container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		mysqlServer.err = err
		return
	}
	mysqlServer.port = port.Port()
	mysqlServer.admin, mysqlServer.err = mysql.Open(mysqlURL("schemigo"))
	if mysqlServer.err == nil {
		mysqlServer.err = mysqlServer.admin.PingContext(ctx)
	}
}

func mysqlURL(database string) string {
	return (&url.URL{Scheme: "mysql", User: url.UserPassword("root", "schemigo"), Host: net.JoinHostPort(mysqlServer.host, mysqlServer.port), Path: "/" + database}).String()
}

// MySQLURL returns a disposable MySQL database and its connection URL.
func MySQLURL(t *testing.T) (*sql.DB, string) {
	t.Helper()
	if os.Getenv("SCHEMIGO_TESTCONTAINERS") != "1" {
		t.Skip("set SCHEMIGO_TESTCONTAINERS=1 to allow disposable database fixtures")
	}
	mysqlServer.once.Do(startMySQL)
	if mysqlServer.err != nil {
		t.Fatalf("start mysql: %v", mysqlServer.err)
	}
	name := "test_" + rand.Text()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := mysqlServer.admin.ExecContext(ctx, "CREATE DATABASE "+mysql.QuoteIdent(name)); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	db, err := mysql.Open(mysqlURL(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := mysqlServer.admin.ExecContext(cleanupCtx, "DROP DATABASE "+mysql.QuoteIdent(name)); err != nil {
			t.Error(err)
		}
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	return db, mysqlURL(name)
}

func stopMySQL() error {
	var err error
	if mysqlServer.admin != nil {
		err = mysqlServer.admin.Close()
	}
	if mysqlServer.container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := mysqlServer.container.Terminate(ctx); e != nil {
			err = fmt.Errorf("%v; terminate: %w", err, e)
		}
	}
	return err
}
