package mysql

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/schemigo/schemigo"
)

func sameTable(a, b schemigo.Table) bool {
	return a.Name == b.Name && slices.EqualFunc(a.Columns, b.Columns, sameColumn) &&
		slices.Equal(a.PrimaryKey, b.PrimaryKey) && reflect.DeepEqual(a.Indexes, b.Indexes) &&
		reflect.DeepEqual(a.ForeignKeys, b.ForeignKeys) && reflect.DeepEqual(a.Checks, b.Checks) &&
		a.Engine == b.Engine && a.OrderBy == b.OrderBy && a.PartitionBy == b.PartitionBy
}

func diffTables(current, desired schemigo.Schema) ([]schemigo.Statement, error) {
	oldTables := make(map[string]schemigo.Table, len(current.Tables))
	for _, table := range current.Tables {
		oldTables[table.Name] = table
	}
	nextTables := make(map[string]schemigo.Table, len(desired.Tables))
	for _, table := range desired.Tables {
		nextTables[table.Name] = table
	}
	var statements, foreignKeys []schemigo.Statement
	for _, table := range current.Tables {
		if _, kept := nextTables[table.Name]; !kept {
			for _, fk := range table.ForeignKeys {
				statements = append(statements, guardedDDL(current.Name, table.Name, "table_constraints", "constraint_name", fk.Name, true, "ALTER TABLE "+Qualified(current.Name, table.Name)+" DROP FOREIGN KEY "+QuoteIdent(fk.Name), true))
			}
		}
	}
	for _, table := range desired.Tables {
		old, found := oldTables[table.Name]
		if !found {
			statements = append(statements, ddl(createTable(desired.Name, table), false))
			for _, fk := range table.ForeignKeys {
				foreignKeys = append(foreignKeys, guardedDDL(desired.Name, table.Name, "table_constraints", "constraint_name", fk.Name, false, addForeignKey(desired.Name, table.Name, fk), false))
			}
			continue
		}
		changes, additions, err := diffTable(desired.Name, old, table)
		if err != nil {
			return nil, err
		}
		statements = append(statements, changes...)
		foreignKeys = append(foreignKeys, additions...)
	}
	for _, table := range current.Tables {
		if _, found := nextTables[table.Name]; !found {
			statements = append(statements, ddl("DROP TABLE IF EXISTS "+Qualified(current.Name, table.Name), true))
		}
	}
	return append(statements, foreignKeys...), nil
}

func createTable(schema string, table schemigo.Table) string {
	parts := make([]string, 0, len(table.Columns)+len(table.Indexes)+len(table.Checks)+1)
	for _, column := range table.Columns {
		parts = append(parts, columnDefinition(column))
	}
	if pk := primaryColumns(table); len(pk) > 0 {
		parts = append(parts, "PRIMARY KEY ("+joinNames(pk)+")")
	}
	for _, index := range table.Indexes {
		parts = append(parts, indexDefinition(index))
	}
	for _, check := range table.Checks {
		parts = append(parts, "CONSTRAINT "+QuoteIdent(check.Name)+" CHECK ("+check.Expression+")")
	}
	return "CREATE TABLE IF NOT EXISTS " + Qualified(schema, table.Name) + " (" + strings.Join(parts, ", ") + ") ENGINE=InnoDB"
}

func columnDefinition(column schemigo.Column) string {
	definition := QuoteIdent(column.Name) + " " + column.Type
	if column.NotNull || column.PrimaryKey {
		definition += " NOT NULL"
	}
	if column.Default != "" {
		definition += " DEFAULT " + column.Default
	}
	return definition
}

func primaryColumns(table schemigo.Table) []string {
	if len(table.PrimaryKey) > 0 {
		return table.PrimaryKey
	}
	var columns []string
	for _, column := range table.Columns {
		if column.PrimaryKey {
			columns = append(columns, column.Name)
		}
	}
	return columns
}

func joinNames(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = QuoteIdent(name)
	}
	return strings.Join(quoted, ", ")
}

func indexDefinition(index schemigo.Index) string {
	prefix := "KEY "
	if index.Unique {
		prefix = "UNIQUE KEY "
	}
	columns := joinNames(index.Columns)
	if index.Expression != "" {
		columns = "(" + index.Expression + ")"
	}
	return prefix + QuoteIdent(index.Name) + " (" + columns + ")"
}

func addForeignKey(schema, table string, fk schemigo.ForeignKey) string {
	command := "ALTER TABLE " + Qualified(schema, table) + " ADD CONSTRAINT " + QuoteIdent(fk.Name) + " FOREIGN KEY (" + joinNames(fk.Columns) + ") REFERENCES " + Qualified(schema, fk.ReferencedTable) + " (" + joinNames(fk.ReferencedColumns) + ")"
	if fk.OnDelete != "" {
		command += " ON DELETE " + fk.OnDelete
	}
	if fk.OnUpdate != "" {
		command += " ON UPDATE " + fk.OnUpdate
	}
	return command
}

func diffTable(schema string, old, next schemigo.Table) ([]schemigo.Statement, []schemigo.Statement, error) {
	ref := Qualified(schema, next.Name)
	var statements, foreignKeys []schemigo.Statement
	oldFK := make(map[string]schemigo.ForeignKey, len(old.ForeignKeys))
	for _, fk := range old.ForeignKeys {
		oldFK[fk.Name] = fk
		newFK, found := findForeignKey(next.ForeignKeys, fk.Name)
		if !found || !reflect.DeepEqual(fk, newFK) {
			statements = append(statements, guardedDDL(schema, next.Name, "table_constraints", "constraint_name", fk.Name, true, "ALTER TABLE "+ref+" DROP FOREIGN KEY "+QuoteIdent(fk.Name), true))
		}
	}
	oldChecks := make(map[string]schemigo.Check, len(old.Checks))
	for _, check := range old.Checks {
		oldChecks[check.Name] = check
		nextCheck, found := findCheck(next.Checks, check.Name)
		if !found || check.Expression != nextCheck.Expression {
			statements = append(statements, guardedDDL(schema, next.Name, "table_constraints", "constraint_name", check.Name, true, "ALTER TABLE "+ref+" DROP CHECK "+QuoteIdent(check.Name), true))
		}
	}
	oldIndexes := make(map[string]schemigo.Index, len(old.Indexes))
	for _, index := range old.Indexes {
		oldIndexes[index.Name] = index
		nextIndex, found := findIndex(next.Indexes, index.Name)
		if !found || !reflect.DeepEqual(index, nextIndex) {
			statements = append(statements, guardedDDL(schema, next.Name, "statistics", "index_name", index.Name, true, "ALTER TABLE "+ref+" DROP INDEX "+QuoteIdent(index.Name), true))
		}
	}
	primaryChanged := !slices.Equal(primaryColumns(old), primaryColumns(next))
	if primaryChanged && len(primaryColumns(old)) > 0 {
		statements = append(statements, guardedDDL(schema, next.Name, "table_constraints", "constraint_name", "PRIMARY", true, "ALTER TABLE "+ref+" DROP PRIMARY KEY", true))
	}
	oldColumns := make(map[string]schemigo.Column, len(old.Columns))
	nextColumns := make(map[string]schemigo.Column, len(next.Columns))
	for _, column := range old.Columns {
		oldColumns[column.Name] = column
	}
	for _, column := range next.Columns {
		nextColumns[column.Name] = column
	}
	renamed := map[string]string{}
	for _, column := range next.Columns {
		if _, found := oldColumns[column.Name]; found {
			continue
		}
		match := ""
		for _, candidate := range old.Columns {
			if _, stillPresent := nextColumns[candidate.Name]; stillPresent || renamed[candidate.Name] != "" {
				continue
			}
			oldName := candidate.Name
			candidate.Name = column.Name
			if reflect.DeepEqual(candidate, column) {
				if match != "" {
					return nil, nil, fmt.Errorf("table %q: ambiguous rename to column %q", next.Name, column.Name)
				}
				match = oldName
			}
		}
		if match != "" {
			renamed[match] = column.Name
			statements = append(statements, guardedDDL(schema, next.Name, "columns", "column_name", match, true, "ALTER TABLE "+ref+" RENAME COLUMN "+QuoteIdent(match)+" TO "+QuoteIdent(column.Name), false))
		} else {
			statements = append(statements, guardedDDL(schema, next.Name, "columns", "column_name", column.Name, false, "ALTER TABLE "+ref+" ADD COLUMN "+columnDefinition(column), false))
		}
	}
	for _, oldColumn := range old.Columns {
		newName := oldColumn.Name
		if name := renamed[oldColumn.Name]; name != "" {
			newName = name
		}
		if nextColumn, found := nextColumns[newName]; found {
			oldColumn.Name = newName
			if !sameColumn(oldColumn, nextColumn) {
				statements = append(statements, guardedDDL(schema, next.Name, "columns", "column_name", newName, true, "ALTER TABLE "+ref+" MODIFY COLUMN "+columnDefinition(nextColumn), true))
			}
		} else {
			statements = append(statements, guardedDDL(schema, next.Name, "columns", "column_name", oldColumn.Name, true, "ALTER TABLE "+ref+" DROP COLUMN "+QuoteIdent(oldColumn.Name), true))
		}
	}
	if primaryChanged {
		if pk := primaryColumns(next); len(pk) > 0 {
			statements = append(statements, guardedDDL(schema, next.Name, "table_constraints", "constraint_name", "PRIMARY", false, "ALTER TABLE "+ref+" ADD PRIMARY KEY ("+joinNames(pk)+")", false))
		}
	}
	for _, index := range next.Indexes {
		if previous, found := oldIndexes[index.Name]; !found || !reflect.DeepEqual(previous, index) {
			statements = append(statements, guardedDDL(schema, next.Name, "statistics", "index_name", index.Name, false, "ALTER TABLE "+ref+" ADD "+indexDefinition(index), false))
		}
	}
	for _, check := range next.Checks {
		if previous, found := oldChecks[check.Name]; !found || previous.Expression != check.Expression {
			statements = append(statements, guardedDDL(schema, next.Name, "table_constraints", "constraint_name", check.Name, false, "ALTER TABLE "+ref+" ADD CONSTRAINT "+QuoteIdent(check.Name)+" CHECK ("+check.Expression+")", false))
		}
	}
	for _, fk := range next.ForeignKeys {
		if previous, found := oldFK[fk.Name]; !found || !reflect.DeepEqual(previous, fk) {
			foreignKeys = append(foreignKeys, guardedDDL(schema, next.Name, "table_constraints", "constraint_name", fk.Name, false, addForeignKey(schema, next.Name, fk), false))
		}
	}
	return statements, foreignKeys, nil
}

func sameColumn(a, b schemigo.Column) bool {
	return canonicalType(a.Type) == canonicalType(b.Type) && a.Name == b.Name && a.NotNull == b.NotNull && a.Default == b.Default && a.PrimaryKey == b.PrimaryKey
}

func canonicalType(value string) string {
	typ := strings.ToLower(strings.Join(strings.Fields(value), " "))
	for _, alias := range []struct{ from, to string }{{"int8", "bigint"}, {"int4", "int"}, {"integer", "int"}, {"int2", "smallint"}, {"bool", "tinyint(1)"}, {"boolean", "tinyint(1)"}, {"numeric", "decimal"}} {
		if typ == alias.from || strings.HasPrefix(typ, alias.from+" ") || strings.HasPrefix(typ, alias.from+"(") {
			return alias.to + strings.TrimPrefix(typ, alias.from)
		}
	}
	return typ
}

func findIndex(indexes []schemigo.Index, name string) (schemigo.Index, bool) {
	for _, index := range indexes {
		if index.Name == name {
			return index, true
		}
	}
	return schemigo.Index{}, false
}

func findCheck(checks []schemigo.Check, name string) (schemigo.Check, bool) {
	for _, check := range checks {
		if check.Name == name {
			return check, true
		}
	}
	return schemigo.Check{}, false
}

func findForeignKey(keys []schemigo.ForeignKey, name string) (schemigo.ForeignKey, bool) {
	for _, key := range keys {
		if key.Name == name {
			return key, true
		}
	}
	return schemigo.ForeignKey{}, false
}

func guardedDDL(schema, table, catalog, nameColumn, name string, present bool, command string, destructive bool) schemigo.Statement {
	query := "EXISTS (SELECT 1 FROM information_schema." + catalog + " WHERE table_schema=" + quoteLiteral(schema) + " AND table_name=" + quoteLiteral(table) + " AND " + nameColumn + "=" + quoteLiteral(name) + ")"
	if !present {
		query = "NOT " + query
	}
	sql := "SET @schemigo_ddl = IF(" + query + ", " + quoteLiteral(command) + ", 'SELECT 1'); PREPARE schemigo_stmt FROM @schemigo_ddl; EXECUTE schemigo_stmt; DEALLOCATE PREPARE schemigo_stmt"
	return ddl(sql, destructive)
}
