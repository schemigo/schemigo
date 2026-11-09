package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

// Inspect reads one schema in a read-only repeatable-read transaction. It returns
// no partial schema on failure. Extension-owned objects and schema_migrations
// are excluded. Properties the model cannot preserve produce an error.
// It leaves the caller's connection open and restores transaction-local settings.
func (*Driver) Inspect(ctx context.Context, conn *sql.Conn, name string) (schema schemigo.Schema, err error) {
	if conn == nil {
		return schema, fmt.Errorf("inspect postgres: nil connection")
	}
	if strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) {
		return schema, fmt.Errorf("inspect postgres: invalid schema name %q", name)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return schema, fmt.Errorf("inspect postgres: begin snapshot: %w", err)
	}
	defer func() {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("inspect postgres: rollback snapshot: %w", rollbackErr))
		}
		if err != nil {
			schema = schemigo.Schema{}
		}
	}()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT FROM pg_catalog.pg_namespace WHERE nspname = $1)`, name).Scan(&exists); err != nil {
		return schema, fmt.Errorf("inspect postgres schema: %w", err)
	}
	if !exists {
		return schema, fmt.Errorf("inspect postgres: schema %q does not exist", name)
	}
	// Deparsing must not depend on the caller's search_path or resolve builtins
	// through another schema. The local setting is restored when the snapshot ends.
	var searchPath string
	if err = tx.QueryRowContext(ctx, `SELECT pg_catalog.set_config(
 'search_path', 'pg_catalog, ' || pg_catalog.quote_ident($1), true)`, name).Scan(&searchPath); err != nil {
		return schema, fmt.Errorf("inspect postgres: set snapshot search_path: %w", err)
	}
	if err = rejectUnsupported(ctx, tx, name); err != nil {
		return schema, err
	}
	schema.Name = name
	if schema.Extensions, err = inspectJSON[schemigo.Extension](ctx, tx, name, extensionsQuery); err != nil {
		return schema, fmt.Errorf("inspect extensions: %w", err)
	}
	if schema.Enums, err = inspectJSON[schemigo.Enum](ctx, tx, name, enumsQuery); err != nil {
		return schema, fmt.Errorf("inspect enums: %w", err)
	}
	if schema.Tables, err = inspectJSON[schemigo.Table](ctx, tx, name, tablesQuery); err != nil {
		return schema, fmt.Errorf("inspect tables: %w", err)
	}
	if schema.Functions, err = inspectJSON[schemigo.Function](ctx, tx, name, functionsQuery); err != nil {
		return schema, fmt.Errorf("inspect functions: %w", err)
	}
	if schema.Triggers, err = inspectTriggers(ctx, tx, name); err != nil {
		return schema, fmt.Errorf("inspect triggers: %w", err)
	}
	if schema.Views, err = inspectJSON[schemigo.View](ctx, tx, name, viewsQuery); err != nil {
		return schema, fmt.Errorf("inspect views: %w", err)
	}
	schema, err = schemigo.Normalize(schema)
	if err != nil {
		return schema, fmt.Errorf("inspect postgres %q: %w", name, err)
	}
	if err = tx.Commit(); err != nil {
		return schema, fmt.Errorf("inspect postgres: commit snapshot: %w", err)
	}
	return schema, nil
}

// Catalog JSON avoids driver-specific PostgreSQL array conversions.
func inspectJSON[T any](ctx context.Context, tx *sql.Tx, name, query string) ([]T, error) {
	rows, err := tx.QueryContext(ctx, query, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []T
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan catalog: %w", err)
		}
		var item T
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, fmt.Errorf("decode catalog: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	return result, nil
}

const extensionsQuery = `
SELECT json_build_object('name', e.extname, 'version', e.extversion)
FROM pg_catalog.pg_extension e
JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace
WHERE n.nspname = $1
ORDER BY e.extname`

const enumsQuery = `
SELECT json_build_object('name', t.typname, 'values',
 (SELECT json_agg(e.enumlabel ORDER BY e.enumsortorder)
  FROM pg_catalog.pg_enum e WHERE e.enumtypid = t.oid))
FROM pg_catalog.pg_type t
JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = $1 AND t.typtype = 'e'
AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend d
 WHERE d.classid = 'pg_catalog.pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
ORDER BY t.typname`
