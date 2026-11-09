//go:build integration

package testdb

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/schemigo/schemigo/postgres"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Postgres starts an isolated PostgreSQL 18 container only when
// SCHEMIGO_TESTCONTAINERS=1. Cleanup closes the database and removes the container.
func Postgres(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("SCHEMIGO_TESTCONTAINERS") != "1" {
		t.Skip("set SCHEMIGO_TESTCONTAINERS=1 to allow disposable database fixtures")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("schemigo"), tcpostgres.WithUsername("schemigo"),
		tcpostgres.WithPassword("schemigo"), tcpostgres.BasicWaitStrategies())
	if container != nil {
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Errorf("remove postgres container: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := postgres.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close postgres: %v", err)
		}
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}
