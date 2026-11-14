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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/schemigo/schemigo/postgres"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var server struct {
	once      sync.Once
	container *tcpostgres.PostgresContainer
	config    *pgx.ConnConfig
	db        *sql.DB
	err       error
}

// Run runs a package's tests and removes its shared PostgreSQL container afterward.
// Call os.Exit(testdb.Run(m)) from TestMain. The container starts lazily in Postgres.
func Run(m *testing.M) int {
	code := m.Run()
	if err := stopMySQL(); err != nil {
		fmt.Fprintln(os.Stderr, "remove mysql container:", err)
		code = 1
	}
	if server.db != nil {
		if err := server.db.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "close postgres admin:", err)
			code = 1
		}
	}
	if server.container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.container.Terminate(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "remove postgres container:", err)
			code = 1
		}
	}
	return code
}

func startPostgres() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	server.container, server.err = tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("schemigo"), tcpostgres.WithUsername("schemigo"),
		tcpostgres.WithPassword("schemigo"), tcpostgres.BasicWaitStrategies())
	if server.err != nil {
		return
	}
	dsn, err := server.container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		server.err = err
		return
	}
	server.config, server.err = pgx.ParseConfig(dsn)
	if server.err != nil {
		return
	}
	server.db = stdlib.OpenDB(*server.config)
	server.err = server.db.PingContext(ctx)
}

// Postgres returns a fresh database on the package's shared PostgreSQL 18 container.
// It skips without SCHEMIGO_TESTCONTAINERS=1. Cleanup closes and drops the database.
// Use Run in TestMain to remove the container. Cluster-wide roles remain shared.
func Postgres(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("SCHEMIGO_TESTCONTAINERS") != "1" {
		t.Skip("set SCHEMIGO_TESTCONTAINERS=1 to allow disposable database fixtures")
	}
	server.once.Do(startPostgres)
	if server.err != nil {
		t.Fatalf("start postgres: %v", server.err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	name := "test_" + rand.Text()
	if _, err := server.db.ExecContext(ctx, "CREATE DATABASE "+postgres.QuoteIdent(name)+" TEMPLATE template0"); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	var db *sql.DB
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Errorf("close postgres: %v", err)
			}
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := server.db.ExecContext(cleanupCtx, "DROP DATABASE "+postgres.QuoteIdent(name)+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})
	config := server.config.Copy()
	config.Database = name
	db = stdlib.OpenDB(*config)
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

// PostgresURL returns a disposable database and its connection URL for CLI tests.
func PostgresURL(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db := Postgres(t)
	var database string
	if err := db.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(server.config.User, server.config.Password), Host: net.JoinHostPort(server.config.Host, strconv.Itoa(int(server.config.Port))), Path: "/" + database, RawQuery: "sslmode=disable"}
	return db, u.String()
}
