package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/schemigo/schemigo/postgres"
)

func TestOpen(t *testing.T) {
	for _, tt := range []struct{ name, dsn string }{
		{name: "URL", dsn: "postgres://user:p%40ss@127.0.0.1:1/db?sslmode=disable"},
		{name: "keyword", dsn: "host=127.0.0.1 port=1 user=user password='p@ss' dbname=db sslmode=disable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, err := postgres.Open(tt.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			})
			if got := db.Stats().OpenConnections; got != 0 {
				t.Fatalf("Open established %d connections before PingContext", got)
			}
		})
	}
}

func TestOpenRejectsInvalidDSN(t *testing.T) {
	for _, tt := range []struct{ name, dsn string }{
		{name: "URL port", dsn: "postgres://user:secret@127.0.0.1:invalid/db"},
		{name: "keyword port", dsn: "host=127.0.0.1 port=invalid user=user password=secret"},
		{name: "TLS mode", dsn: "host=127.0.0.1 user=user password=secret sslmode=invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, err := postgres.Open(tt.dsn)
			var parseErr *pgconn.ParseConfigError
			if db != nil || !errors.As(err, &parseErr) {
				t.Fatalf("db=%v, error=%v; want nil database and parse error", db, err)
			}
			if !strings.HasPrefix(err.Error(), "open postgres:") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unexpected error context or exposed password: %v", err)
			}
		})
	}
}
