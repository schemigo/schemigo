package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"

	"github.com/schemigo/schemigo"
)

func migrationTable(schema string) (string, error) {
	if err := postgresName(schema); err != nil {
		return "", err
	}
	return Qualified(schema, "schema_migrations"), nil
}

// Validate asks PostgreSQL to parse and plan explainable statements.
func (*Driver) Validate(ctx context.Context, conn *sql.Conn, statement schemigo.Statement) error {
	if statement.SkipExplain {
		return ctx.Err()
	}
	rows, err := conn.QueryContext(ctx, "EXPLAIN "+statement.SQL)
	if err != nil {
		return fmt.Errorf("explain statement: %w", err)
	}
	return rows.Close()
}

func lockKey(value string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return int32(h.Sum32())
}

// Lock serializes migrations for one database and schema on this connection.
func (*Driver) Lock(ctx context.Context, conn *sql.Conn, schema string) (schemigo.UnlockFunc, error) {
	if _, err := migrationTable(schema); err != nil {
		return nil, err
	}
	var database string
	if err := conn.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil {
		return nil, fmt.Errorf("database for advisory lock: %w", err)
	}
	a, b := lockKey(database), lockKey(schema)
	if _, err := conn.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_lock($1, $2)", a, b); err != nil {
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}
	return func(ctx context.Context) error {
		var unlocked bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_catalog.pg_advisory_unlock($1, $2)", a, b).Scan(&unlocked); err != nil {
			return fmt.Errorf("release advisory lock: %w", err)
		}
		if !unlocked {
			return fmt.Errorf("advisory lock was not held")
		}
		return nil
	}, nil
}

// EnsureHistory creates the migration log inside the managed namespace.
func (*Driver) EnsureHistory(ctx context.Context, conn *sql.Conn, schema string) error {
	table, err := migrationTable(schema)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, hash text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now(), applied_by text NOT NULL DEFAULT current_user)")
	return err
}

// HasMigration reports whether a plan hash has ever been recorded.
func (*Driver) HasMigration(ctx context.Context, conn *sql.Conn, schema, hash string) (bool, error) {
	table, err := migrationTable(schema)
	if err != nil {
		return false, err
	}
	var exists bool
	err = conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT FROM "+table+" WHERE hash=$1)", hash).Scan(&exists)
	return exists, err
}

// RecordMigration appends a plan hash with the database clock and current user.
func (*Driver) RecordMigration(ctx context.Context, conn *sql.Conn, schema string, migration schemigo.Migration) error {
	table, err := migrationTable(schema)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO "+table+" (hash) VALUES ($1)", migration.Hash)
	return err
}

// RecordMigrationTx records ordinary SQL atomically with its migration history.
func (*Driver) RecordMigrationTx(ctx context.Context, tx *sql.Tx, schema string, migration schemigo.Migration) error {
	table, err := migrationTable(schema)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO "+table+" (hash) VALUES ($1)", migration.Hash)
	return err
}

// ListMigrations returns history in insertion order, or an empty list if absent.
func (*Driver) ListMigrations(ctx context.Context, conn *sql.Conn, schema string) ([]schemigo.Migration, error) {
	table, err := migrationTable(schema)
	if err != nil {
		return nil, err
	}
	var present bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_catalog.to_regclass($1) IS NOT NULL", table).Scan(&present); err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT hash,applied_at,applied_by FROM "+table+" ORDER BY id")
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
