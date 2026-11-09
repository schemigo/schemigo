package schemigo

import (
	"fmt"
	"slices"
	"strings"
)

// Normalize returns an independent copy with default index methods, trigger
// modes and entity order. It preserves object and column order and raw SQL.
// Missing entity kinds are appended to Order in the default order. It rejects
// empty/NUL identifiers, duplicates, invalid local references, conflicting
// primary key declarations and contradictory trigger modes. An empty Schema is
// valid; dialect-specific SQL and capabilities are checked by the driver.
func Normalize(input Schema) (Schema, error) {
	s := input
	s.Extensions = slices.Clone(s.Extensions)
	s.Enums = slices.Clone(s.Enums)
	s.Tables = slices.Clone(s.Tables)
	s.Functions = slices.Clone(s.Functions)
	s.Triggers = slices.Clone(s.Triggers)
	s.Views = slices.Clone(s.Views)
	s.Order = slices.Clone(s.Order)
	if s.Name != "" {
		if err := validName(s.Name); err != nil {
			return Schema{}, fmt.Errorf("schema: %w", err)
		}
	}
	extensions := nameSet{}
	for _, e := range s.Extensions {
		if err := extensions.add(e.Name); err != nil {
			return Schema{}, fmt.Errorf("extension: %w", err)
		}
	}
	enums := nameSet{}
	for i := range s.Enums {
		e := &s.Enums[i]
		e.Values = slices.Clone(e.Values)
		if err := enums.add(e.Name); err != nil {
			return Schema{}, fmt.Errorf("enum: %w", err)
		}
		if len(e.Values) == 0 {
			return Schema{}, fmt.Errorf("enum %q: values are empty", e.Name)
		}
		values := map[string]bool{}
		for _, v := range e.Values {
			if values[v] {
				return Schema{}, fmt.Errorf("enum %q: duplicate value %q", e.Name, v)
			}
			values[v] = true
		}
	}
	tables := map[string]Table{}
	tableNames := nameSet{}
	for i := range s.Tables {
		t := &s.Tables[i]
		if err := tableNames.add(t.Name); err != nil {
			return Schema{}, fmt.Errorf("table: %w", err)
		}
		if err := normalizeTable(t); err != nil {
			return Schema{}, fmt.Errorf("table %q: %w", t.Name, err)
		}
		tables[t.Name] = *t
	}
	for _, t := range s.Tables {
		for _, fk := range t.ForeignKeys {
			target, ok := tables[fk.ReferencedTable]
			if !ok {
				return Schema{}, fmt.Errorf("table %q foreign key %q: unknown referenced table %q", t.Name, fk.Name, fk.ReferencedTable)
			}
			if err := checkColumns(fk.ReferencedColumns, target.Columns); err != nil {
				return Schema{}, fmt.Errorf("table %q foreign key %q: %w", t.Name, fk.Name, err)
			}
		}
	}
	functions := nameSet{}
	functionNames := map[string]bool{}
	for _, f := range s.Functions {
		if err := validName(f.Name); err != nil {
			return Schema{}, fmt.Errorf("function: %w", err)
		}
		// shortcut: compare Parameters as text; canonicalize signatures in the dialect driver.
		key := f.Name + "\x00" + strings.TrimSpace(f.Parameters)
		if _, ok := functions[key]; ok {
			return Schema{}, fmt.Errorf("duplicate function %q with parameters %q", f.Name, f.Parameters)
		}
		functions[key] = struct{}{}
		functionNames[f.Name] = true
	}
	views := nameSet{}
	triggerViews := nameSet{}
	for _, v := range s.Views {
		if err := views.add(v.Name); err != nil {
			return Schema{}, fmt.Errorf("view: %w", err)
		}
		if _, ok := tables[v.Name]; ok {
			return Schema{}, fmt.Errorf("view %q conflicts with table name", v.Name)
		}
		if !v.Materialized {
			triggerViews[v.Name] = struct{}{}
		}
	}
	triggers := map[string]nameSet{}
	for i := range s.Triggers {
		tr := &s.Triggers[i]
		_, tableExists := tables[tr.Table]
		_, viewExists := triggerViews[tr.Table]
		if !tableExists && !viewExists {
			return Schema{}, fmt.Errorf("trigger %q: unknown table or ordinary view %q", tr.Name, tr.Table)
		}
		if triggers[tr.Table] == nil {
			triggers[tr.Table] = nameSet{}
		}
		if err := triggers[tr.Table].add(tr.Name); err != nil {
			return Schema{}, fmt.Errorf("trigger on %q: %w", tr.Table, err)
		}
		if !functionNames[tr.Function] {
			return Schema{}, fmt.Errorf("trigger %q: unknown function %q", tr.Name, tr.Function)
		}
		if tr.ForEach == "" {
			tr.ForEach = "ROW"
		}
		if tr.ForEach != "ROW" && tr.ForEach != "STATEMENT" {
			return Schema{}, fmt.Errorf("trigger %q: invalid ForEach %q", tr.Name, tr.ForEach)
		}
		if tr.ForEach == "STATEMENT" && tr.ForEachRow {
			return Schema{}, fmt.Errorf("trigger %q: ForEach conflicts with ForEachRow", tr.Name)
		}
		tr.ForEachRow = tr.ForEach == "ROW"
	}
	defaults := []EntityKind{EntityExtensions, EntityEnums, EntityTables, EntityFunctions, EntityTriggers, EntityViews}
	seen := map[EntityKind]bool{}
	for _, kind := range s.Order {
		if !slices.Contains(defaults, kind) || seen[kind] {
			return Schema{}, fmt.Errorf("order: invalid or duplicate entity kind %q", kind)
		}
		seen[kind] = true
	}
	for _, kind := range defaults {
		if !seen[kind] {
			s.Order = append(s.Order, kind)
		}
	}
	return s, nil
}

type nameSet map[string]struct{}

func validName(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid name %q", name)
	}
	return nil
}

func (names nameSet) add(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	if _, ok := names[name]; ok {
		return fmt.Errorf("duplicate name %q", name)
	}
	names[name] = struct{}{}
	return nil
}

func normalizeTable(t *Table) error {
	t.Columns = slices.Clone(t.Columns)
	t.PrimaryKey = slices.Clone(t.PrimaryKey)
	t.Indexes = slices.Clone(t.Indexes)
	t.ForeignKeys = slices.Clone(t.ForeignKeys)
	t.Checks = slices.Clone(t.Checks)
	columns := nameSet{}
	columnPK := 0
	for _, c := range t.Columns {
		if err := columns.add(c.Name); err != nil {
			return fmt.Errorf("column: %w", err)
		}
		if strings.TrimSpace(c.Type) == "" {
			return fmt.Errorf("column %q: type is empty", c.Name)
		}
		if c.PrimaryKey {
			columnPK++
		}
	}
	if len(t.PrimaryKey) > 0 && columnPK > 0 {
		return fmt.Errorf("primary key: use either Table.PrimaryKey or Column.PrimaryKey")
	}
	if columnPK > 1 {
		return fmt.Errorf("primary key: use Table.PrimaryKey for ordered composite keys")
	}
	if err := checkColumns(t.PrimaryKey, t.Columns); err != nil {
		return fmt.Errorf("primary key: %w", err)
	}
	indexes := nameSet{}
	for i := range t.Indexes {
		idx := &t.Indexes[i]
		idx.Columns = slices.Clone(idx.Columns)
		if err := indexes.add(idx.Name); err != nil {
			return fmt.Errorf("index: %w", err)
		}
		if (len(idx.Columns) > 0) == (strings.TrimSpace(idx.Expression) != "") {
			return fmt.Errorf("index %q: specify exactly one of Columns and Expression", idx.Name)
		}
		if err := checkColumns(idx.Columns, t.Columns); err != nil {
			return fmt.Errorf("index %q: %w", idx.Name, err)
		}
		if idx.Method == "" {
			idx.Method = "btree"
		}
	}
	// SQL constraint names share a namespace within a table.
	constraints := nameSet{}
	for i := range t.ForeignKeys {
		fk := &t.ForeignKeys[i]
		fk.Columns = slices.Clone(fk.Columns)
		fk.ReferencedColumns = slices.Clone(fk.ReferencedColumns)
		if err := constraints.add(fk.Name); err != nil {
			return fmt.Errorf("foreign key: %w", err)
		}
		if len(fk.Columns) == 0 || len(fk.Columns) != len(fk.ReferencedColumns) {
			return fmt.Errorf("foreign key %q: columns must be nonempty and paired", fk.Name)
		}
		if err := checkColumns(fk.Columns, t.Columns); err != nil {
			return fmt.Errorf("foreign key %q: %w", fk.Name, err)
		}
	}
	for _, c := range t.Checks {
		if err := constraints.add(c.Name); err != nil {
			return fmt.Errorf("check: %w", err)
		}
		if strings.TrimSpace(c.Expression) == "" {
			return fmt.Errorf("check %q: expression is empty", c.Name)
		}
	}
	return nil
}

func checkColumns(names []string, columns []Column) error {
	seen := nameSet{}
	for _, name := range names {
		if err := seen.add(name); err != nil {
			return err
		}
		found := false
		for _, column := range columns {
			if column.Name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown column %q", name)
		}
	}
	return nil
}
