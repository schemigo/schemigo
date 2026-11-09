// Package postgres implements PostgreSQL schema inspection.
package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/schemigo/schemigo"
)

// Driver reads PostgreSQL catalogs. Migration operations are not implemented yet
// and return errors.ErrUnsupported. Its zero value is ready for inspection.
type Driver struct{}

var _ schemigo.Driver = (*Driver)(nil)

// New returns a PostgreSQL driver. The caller owns its database connections.
func New() schemigo.Driver { return &Driver{} }

// Name returns the dialect identifier.
func (*Driver) Name() string { return "postgres" }

// Diff is not implemented yet.
func (*Driver) Diff(context.Context, *sql.Conn, schemigo.Schema, schemigo.Schema) (schemigo.Plan, error) {
	return schemigo.Plan{}, errors.ErrUnsupported
}

// Validate is not implemented yet.
func (*Driver) Validate(context.Context, *sql.Conn, schemigo.Statement) error {
	return errors.ErrUnsupported
}

// Lock is not implemented yet.
func (*Driver) Lock(context.Context, *sql.Conn, string) (schemigo.UnlockFunc, error) {
	return nil, errors.ErrUnsupported
}

// EnsureHistory is not implemented yet.
func (*Driver) EnsureHistory(context.Context, *sql.Conn, string) error { return errors.ErrUnsupported }

// HasMigration is not implemented yet.
func (*Driver) HasMigration(context.Context, *sql.Conn, string, string) (bool, error) {
	return false, errors.ErrUnsupported
}

// RecordMigration is not implemented yet.
func (*Driver) RecordMigration(context.Context, *sql.Conn, string, schemigo.Migration) error {
	return errors.ErrUnsupported
}

// ListMigrations is not implemented yet.
func (*Driver) ListMigrations(context.Context, *sql.Conn, string) ([]schemigo.Migration, error) {
	return nil, errors.ErrUnsupported
}
