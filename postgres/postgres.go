// Package postgres implements PostgreSQL schema inspection and migration planning.
package postgres

import (
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/schemigo/schemigo"
)

// Driver inspects PostgreSQL and plans changes to tables, constraints, enums,
// extensions, functions, triggers and views. Its zero value is ready for use.
type Driver struct{}

var _ schemigo.Driver = (*Driver)(nil)

// New returns a PostgreSQL driver. The caller owns its database connections.
func New() schemigo.Driver { return &Driver{} }

// Open parses a PostgreSQL URL or keyword DSN and returns a caller-owned pool.
// It uses pgx defaults, including environment variables, and does not connect.
// Use PingContext to check connectivity, and Close to release the pool.
func Open(dsn string) (*sql.DB, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return stdlib.OpenDB(*config), nil
}

// Name returns the dialect identifier.
func (*Driver) Name() string { return "postgres" }
