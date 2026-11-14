package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/schemigo/schemigo"
)

// Validate checks cancellation; ClickHouse DDL is validated by execution.
func (*Driver) Validate(ctx context.Context, _ *sql.Conn, _ schemigo.Statement) error {
	return ctx.Err()
}

// Lock requires an external coordinator before any database DDL.
func (d *Driver) Lock(ctx context.Context, _ *sql.Conn, database string) (schemigo.UnlockFunc, error) {
	if err := validName(database); err != nil {
		return nil, err
	}
	if d.locker == nil {
		return nil, fmt.Errorf("ClickHouse Apply requires a Keeper/ZooKeeper locker (-keeper)")
	}
	// SQL functions are server-global, so database migrations share one lock.
	return d.locker.Lock(ctx, "schema")
}

// MigrationContext supplies Apply with a context canceled on coordinator loss.
// A custom Locker must expose the same optional method to signal session loss.
func (d *Driver) MigrationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if l, ok := d.locker.(interface {
		MigrationContext(context.Context) (context.Context, context.CancelFunc)
	}); ok {
		return l.MigrationContext(ctx)
	}
	return context.WithCancel(ctx)
}

// EnsureHistory creates an append-only MergeTree migration journal.
func (*Driver) EnsureHistory(ctx context.Context, conn *sql.Conn, database string) error {
	if err := validName(database); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+Qualified(database, "schema_migrations")+" (hash String, applied_at DateTime64(9, 'UTC'), applied_by String) ENGINE = MergeTree ORDER BY (applied_at, hash)")
	return err
}

// HasMigration reports previous occurrences; it does not authorize skipping drift.
func (*Driver) HasMigration(ctx context.Context, conn *sql.Conn, database, hash string) (bool, error) {
	if err := validName(database); err != nil {
		return false, err
	}
	var n uint64
	err := conn.QueryRowContext(ctx, "SELECT count() FROM "+Qualified(database, "schema_migrations")+" WHERE hash = ?", hash).Scan(&n)
	return n > 0, err
}

// RecordMigration appends a successful migration using the server clock and user.
func (*Driver) RecordMigration(ctx context.Context, conn *sql.Conn, database string, m schemigo.Migration) error {
	if err := validName(database); err != nil {
		return err
	}
	// INSERT SELECT is synchronous even if async_insert is enabled on the connection.
	_, err := conn.ExecContext(ctx, "INSERT INTO "+Qualified(database, "schema_migrations")+" SELECT ?, now64(9, 'UTC'), currentUser()", m.Hash)
	return err
}

// ListMigrations reads the journal in timestamp order, or returns an empty list.
func (*Driver) ListMigrations(ctx context.Context, conn *sql.Conn, database string) ([]schemigo.Migration, error) {
	if err := validName(database); err != nil {
		return nil, err
	}
	var n uint64
	if err := conn.QueryRowContext(ctx, "SELECT count() FROM system.tables WHERE database = ? AND name = 'schema_migrations'", database).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT hash, applied_at, applied_by FROM "+Qualified(database, "schema_migrations")+" ORDER BY applied_at, hash, applied_by")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []schemigo.Migration
	for rows.Next() {
		var m schemigo.Migration
		if err := rows.Scan(&m.Hash, &m.AppliedAt, &m.User); err != nil {
			return nil, err
		}
		m.AppliedAt = m.AppliedAt.UTC()
		result = append(result, m)
	}
	return result, rows.Err()
}
