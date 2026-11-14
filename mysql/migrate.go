package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

// Validate checks a planned MySQL statement. DDL cannot be explained safely.
func (*Driver) Validate(ctx context.Context, conn *sql.Conn, statement schemigo.Statement) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(statement.SQL) == "" {
		return fmt.Errorf("validate mysql: statement is empty")
	}
	if statement.SkipExplain {
		return nil
	}
	if conn == nil {
		return fmt.Errorf("validate mysql: connection is required")
	}
	rows, err := conn.QueryContext(ctx, "EXPLAIN "+statement.SQL)
	if err != nil {
		return fmt.Errorf("explain mysql statement: %w", err)
	}
	return rows.Close()
}

// Lock acquires a MySQL advisory lock.
func (*Driver) Lock(ctx context.Context, conn *sql.Conn, schema string) (schemigo.UnlockFunc, error) {
	if err := mysqlName(schema); err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, fmt.Errorf("lock mysql: connection is required")
	}
	sum := sha256.Sum256([]byte(schema))
	name := "schemigo:" + hex.EncodeToString(sum[:16])
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", name).Scan(&acquired); err != nil {
		return nil, fmt.Errorf("acquire mysql lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return nil, fmt.Errorf("acquire mysql lock: timed out or unavailable")
	}
	return func(ctx context.Context) error {
		var released sql.NullInt64
		if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&released); err != nil {
			return fmt.Errorf("release mysql lock: %w", err)
		}
		if !released.Valid || released.Int64 != 1 {
			return fmt.Errorf("mysql lock was not held")
		}
		return nil
	}, nil
}

// EnsureHistory creates the migration history table.
func (*Driver) EnsureHistory(ctx context.Context, conn *sql.Conn, schema string) error {
	table, err := migrationTable(schema)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, hash CHAR(64) NOT NULL, applied_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), actor VARCHAR(255) NOT NULL) ENGINE=InnoDB")
	return err
}

// HasMigration reports whether the hash was applied before.
func (*Driver) HasMigration(ctx context.Context, conn *sql.Conn, schema, hash string) (bool, error) {
	table, err := migrationTable(schema)
	if err != nil {
		return false, err
	}
	var exists bool
	err = conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE hash=?)", hash).Scan(&exists)
	return exists, err
}

// RecordMigration records a successful migration.
func (*Driver) RecordMigration(ctx context.Context, conn *sql.Conn, schema string, migration schemigo.Migration) error {
	table, err := migrationTable(schema)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO "+table+" (hash, actor) VALUES (?, CURRENT_USER())", migration.Hash)
	return err
}

// ListMigrations reads migration history.
func (*Driver) ListMigrations(ctx context.Context, conn *sql.Conn, schema string) ([]schemigo.Migration, error) {
	if _, err := migrationTable(schema); err != nil {
		return nil, err
	}
	var exists bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=? AND table_name='schema_migrations')", schema).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT hash, applied_at, actor FROM "+Qualified(schema, "schema_migrations")+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []schemigo.Migration
	for rows.Next() {
		var event schemigo.Migration
		if err := rows.Scan(&event.Hash, &event.AppliedAt, &event.User); err != nil {
			return nil, err
		}
		event.AppliedAt = event.AppliedAt.UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}

func migrationTable(schema string) (string, error) {
	if err := mysqlName(schema); err != nil {
		return "", err
	}
	return Qualified(schema, "schema_migrations"), nil
}

func mysqlName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 64 || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid MySQL identifier %q", name)
	}
	return nil
}
