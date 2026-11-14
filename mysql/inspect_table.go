package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

func inspectTable(ctx context.Context, conn *sql.Conn, schema, name string) (schemigo.Table, error) {
	table := schemigo.Table{Name: name}
	rows, err := conn.QueryContext(ctx, "SELECT column_name, column_type, is_nullable, column_default, extra, generation_expression FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position", schema, name)
	if err != nil {
		return table, err
	}
	for rows.Next() {
		var columnName, columnType, nullable, extra string
		var defaultValue, generation sql.NullString
		if err := rows.Scan(&columnName, &columnType, &nullable, &defaultValue, &extra, &generation); err != nil {
			rows.Close()
			return table, err
		}
		if generation.String != "" {
			rows.Close()
			return table, fmt.Errorf("column %q: generated columns are unsupported", columnName)
		}
		column := schemigo.Column{Name: columnName, Type: columnType, NotNull: nullable == "NO"}
		if strings.Contains(strings.ToLower(extra), "auto_increment") {
			column.Type += " AUTO_INCREMENT"
		}
		if defaultValue.Valid {
			column.Default = defaultValue.String
			if isStringType(columnType) && !strings.Contains(strings.ToLower(extra), "default_generated") {
				column.Default = quoteLiteral(defaultValue.String)
			}
		}
		table.Columns = append(table.Columns, column)
	}
	if err := closeRows(rows); err != nil {
		return table, err
	}
	rows, err = conn.QueryContext(ctx, "SELECT column_name FROM information_schema.key_column_usage WHERE table_schema=? AND table_name=? AND constraint_name='PRIMARY' ORDER BY ordinal_position", schema, name)
	if err != nil {
		return table, err
	}
	var primary []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			rows.Close()
			return table, err
		}
		primary = append(primary, column)
	}
	if err := closeRows(rows); err != nil {
		return table, err
	}
	if len(primary) == 1 {
		for i := range table.Columns {
			if table.Columns[i].Name == primary[0] {
				table.Columns[i].PrimaryKey = true
			}
		}
	} else {
		table.PrimaryKey = primary
	}
	rows, err = conn.QueryContext(ctx, "SELECT index_name, non_unique, column_name, expression, index_type FROM information_schema.statistics WHERE table_schema=? AND table_name=? AND index_name <> 'PRIMARY' ORDER BY index_name, seq_in_index", schema, name)
	if err != nil {
		return table, err
	}
	for rows.Next() {
		var indexName, indexType string
		var nonUnique int
		var column, expression sql.NullString
		if err := rows.Scan(&indexName, &nonUnique, &column, &expression, &indexType); err != nil {
			rows.Close()
			return table, err
		}
		if !strings.EqualFold(indexType, "BTREE") {
			rows.Close()
			return table, fmt.Errorf("index %q: unsupported method %q", indexName, indexType)
		}
		if len(table.Indexes) == 0 || table.Indexes[len(table.Indexes)-1].Name != indexName {
			table.Indexes = append(table.Indexes, schemigo.Index{Name: indexName, Unique: nonUnique == 0})
		}
		index := &table.Indexes[len(table.Indexes)-1]
		switch {
		case column.Valid && !expression.Valid && index.Expression == "":
			index.Columns = append(index.Columns, column.String)
		case expression.Valid && !column.Valid && len(index.Columns) == 0 && index.Expression == "":
			index.Expression = expression.String
		default:
			rows.Close()
			return table, fmt.Errorf("index %q: mixed or compound expression is unsupported", indexName)
		}
	}
	if err := closeRows(rows); err != nil {
		return table, err
	}
	rows, err = conn.QueryContext(ctx, `SELECT k.constraint_name, k.column_name, k.referenced_table_name, k.referenced_column_name, r.delete_rule, r.update_rule
FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r
ON r.constraint_schema=k.constraint_schema AND r.constraint_name=k.constraint_name AND r.table_name=k.table_name
WHERE k.table_schema=? AND k.table_name=? AND k.referenced_table_name IS NOT NULL
ORDER BY k.constraint_name, k.ordinal_position`, schema, name)
	if err != nil {
		return table, err
	}
	for rows.Next() {
		var constraint, column, target, targetColumn, onDelete, onUpdate string
		if err := rows.Scan(&constraint, &column, &target, &targetColumn, &onDelete, &onUpdate); err != nil {
			rows.Close()
			return table, err
		}
		if len(table.ForeignKeys) == 0 || table.ForeignKeys[len(table.ForeignKeys)-1].Name != constraint {
			table.ForeignKeys = append(table.ForeignKeys, schemigo.ForeignKey{Name: constraint, ReferencedTable: target, OnDelete: defaultAction(onDelete), OnUpdate: defaultAction(onUpdate)})
		}
		fk := &table.ForeignKeys[len(table.ForeignKeys)-1]
		fk.Columns = append(fk.Columns, column)
		fk.ReferencedColumns = append(fk.ReferencedColumns, targetColumn)
	}
	if err := closeRows(rows); err != nil {
		return table, err
	}
	rows, err = conn.QueryContext(ctx, `SELECT tc.constraint_name, cc.check_clause FROM information_schema.table_constraints tc
JOIN information_schema.check_constraints cc ON cc.constraint_schema=tc.constraint_schema AND cc.constraint_name=tc.constraint_name
WHERE tc.table_schema=? AND tc.table_name=? AND tc.constraint_type='CHECK' ORDER BY tc.constraint_name`, schema, name)
	if err != nil {
		return table, err
	}
	for rows.Next() {
		var check schemigo.Check
		if err := rows.Scan(&check.Name, &check.Expression); err != nil {
			rows.Close()
			return table, err
		}
		table.Checks = append(table.Checks, check)
	}
	if err := closeRows(rows); err != nil {
		return table, err
	}
	return table, nil
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	return errors.Join(err, rows.Close())
}

func isStringType(columnType string) bool {
	columnType = strings.ToLower(columnType)
	for _, prefix := range []string{"char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set", "date"} {
		if strings.HasPrefix(columnType, prefix) {
			return true
		}
	}
	return false
}

func defaultAction(action string) string {
	if action == "RESTRICT" || action == "NO ACTION" {
		return ""
	}
	return action
}
