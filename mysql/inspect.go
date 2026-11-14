package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

// Inspect reads supported objects from a MySQL database.
func (*Driver) Inspect(ctx context.Context, conn *sql.Conn, schemaName string) (schemigo.Schema, error) {
	if err := ctx.Err(); err != nil {
		return schemigo.Schema{}, err
	}
	if err := mysqlName(schemaName); err != nil {
		return schemigo.Schema{}, err
	}
	if conn == nil {
		return schemigo.Schema{}, fmt.Errorf("inspect mysql: connection is required")
	}
	schema := schemigo.Schema{Name: schemaName}
	rows, err := conn.QueryContext(ctx, "SELECT table_name, table_type, engine FROM information_schema.tables WHERE table_schema=? AND table_name <> 'schema_migrations' ORDER BY table_name", schemaName)
	if err != nil {
		return schemigo.Schema{}, fmt.Errorf("inspect mysql tables: %w", err)
	}
	type object struct{ name, kind string }
	var objects []object
	for rows.Next() {
		var name, kind string
		var engine sql.NullString
		if err := rows.Scan(&name, &kind, &engine); err != nil {
			rows.Close()
			return schemigo.Schema{}, err
		}
		if kind == "BASE TABLE" && !strings.EqualFold(engine.String, "InnoDB") {
			rows.Close()
			return schemigo.Schema{}, fmt.Errorf("table %q: unsupported engine %q", name, engine.String)
		}
		objects = append(objects, object{name, kind})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return schemigo.Schema{}, err
	}
	if err := rows.Close(); err != nil {
		return schemigo.Schema{}, err
	}
	for _, object := range objects {
		switch object.kind {
		case "BASE TABLE":
			table, err := inspectTable(ctx, conn, schemaName, object.name)
			if err != nil {
				return schemigo.Schema{}, fmt.Errorf("inspect table %q: %w", object.name, err)
			}
			schema.Tables = append(schema.Tables, table)
		case "VIEW":
			var definition string
			if err := conn.QueryRowContext(ctx, "SELECT view_definition FROM information_schema.views WHERE table_schema=? AND table_name=?", schemaName, object.name).Scan(&definition); err != nil {
				return schemigo.Schema{}, fmt.Errorf("inspect view %q: %w", object.name, err)
			}
			schema.Views = append(schema.Views, schemigo.View{Name: object.name, Definition: definition})
		default:
			return schemigo.Schema{}, fmt.Errorf("unsupported MySQL object %q of type %q", object.name, object.kind)
		}
	}
	functions, err := inspectRoutines(ctx, conn, schemaName)
	if err != nil {
		return schemigo.Schema{}, err
	}
	schema.Functions = functions
	triggers, err := inspectTriggers(ctx, conn, schemaName)
	if err != nil {
		return schemigo.Schema{}, err
	}
	schema.Triggers = triggers
	normalized, err := schemigo.Normalize(schema)
	if err != nil {
		return schemigo.Schema{}, fmt.Errorf("inspect mysql: %w", err)
	}
	return normalized, nil
}
