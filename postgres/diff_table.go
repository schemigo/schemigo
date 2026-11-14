package postgres

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/schemigo/schemigo"
)

func tableDDL(sql string, destructive bool) schemigo.Statement {
	return schemigo.Statement{SQL: sql, Destructive: destructive, SkipExplain: true}
}

func matchRenames[T any](before, after []T, name func(T) string, equal func(T, T) bool) (map[string]string, error) {
	oldNames, newNames := map[string]bool{}, map[string]bool{}
	for _, v := range before {
		oldNames[name(v)] = true
	}
	for _, v := range after {
		newNames[name(v)] = true
	}
	result, used := map[string]string{}, map[string]string{}
	for _, old := range before {
		if newNames[name(old)] {
			continue
		}
		candidates := []string{}
		for _, next := range after {
			if !oldNames[name(next)] && equal(old, next) {
				candidates = append(candidates, name(next))
			}
		}
		if len(candidates) > 1 {
			return nil, fmt.Errorf("ambiguous rename of %q: %v", name(old), candidates)
		}
		if len(candidates) == 1 {
			target := candidates[0]
			if previous, ok := used[target]; ok {
				return nil, fmt.Errorf("ambiguous rename to %q: %q or %q", target, previous, name(old))
			}
			result[name(old)] = target
			used[target] = name(old)
		}
	}
	return result, nil
}

func canonicalTableProperties(s *schemigo.Schema) {
	for i := range s.Tables {
		t := &s.Tables[i]
		slices.SortFunc(t.Indexes, func(a, b schemigo.Index) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(t.ForeignKeys, func(a, b schemigo.ForeignKey) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(t.Checks, func(a, b schemigo.Check) int { return strings.Compare(a.Name, b.Name) })
		for j := range t.ForeignKeys {
			t.ForeignKeys[j].OnDelete = strings.ToUpper(strings.Join(strings.Fields(t.ForeignKeys[j].OnDelete), " "))
			t.ForeignKeys[j].OnUpdate = strings.ToUpper(strings.Join(strings.Fields(t.ForeignKeys[j].OnUpdate), " "))
			if t.ForeignKeys[j].OnDelete == "" {
				t.ForeignKeys[j].OnDelete = "NO ACTION"
			}
			if t.ForeignKeys[j].OnUpdate == "" {
				t.ForeignKeys[j].OnUpdate = "NO ACTION"
			}
		}
		if len(t.PrimaryKey) == 0 {
			for _, c := range t.Columns {
				if c.PrimaryKey {
					t.PrimaryKey = []string{c.Name}
				}
			}
		}
		for j := range t.Columns {
			t.Columns[j].PrimaryKey = false
			if slices.Contains(t.PrimaryKey, t.Columns[j].Name) {
				t.Columns[j].NotNull = true
			}
		}
	}
}

func columnShape(s schemigo.Schema, c schemigo.Column) schemigo.Column {
	c.Name = ""
	c.Using = ""
	c.Type = columnTypeKey(s, c.Type)
	c.Default = defaultKey(s, c.Default, c.Type)
	return c
}
func tableShape(s schemigo.Schema, t schemigo.Table) schemigo.Table {
	self := t.Name
	t.Name = ""
	t.Columns = slices.Clone(t.Columns)
	t.ForeignKeys = slices.Clone(t.ForeignKeys)
	t.Indexes = slices.Clone(t.Indexes)
	t.Checks = slices.Clone(t.Checks)
	for i := range t.Indexes {
		t.Indexes[i].Expression = expressionKey(t.Indexes[i].Expression)
		t.Indexes[i].Where = expressionKey(t.Indexes[i].Where)
	}
	for i := range t.Checks {
		t.Checks[i].Expression = expressionKey(t.Checks[i].Expression)
	}
	for i := range t.ForeignKeys {
		if t.ForeignKeys[i].ReferencedTable == self {
			t.ForeignKeys[i].ReferencedTable = ""
		}
	}
	for i, c := range t.Columns {
		name := c.Name
		t.Columns[i] = columnShape(s, c)
		t.Columns[i].Name = name
	}
	return t
}

func diffTables(current *schemigo.Schema, desired schemigo.Schema) ([]schemigo.Statement, error) {
	for _, s := range []schemigo.Schema{*current, desired} {
		if err := validateTableObjects(s); err != nil {
			return nil, err
		}
		for _, t := range s.Tables {
			if err := postgresName(t.Name); err != nil {
				return nil, err
			}
			if t.Name == "schema_migrations" {
				return nil, fmt.Errorf("schema_migrations is reserved")
			}
			if t.Engine != "" || t.OrderBy != "" || t.PartitionBy != "" {
				return nil, fmt.Errorf("PostgreSQL table options: %w", errors.ErrUnsupported)
			}
			for _, c := range t.Columns {
				if err := postgresName(c.Name); err != nil {
					return nil, err
				}
			}
		}
	}
	renames, err := matchRenames(current.Tables, desired.Tables, func(t schemigo.Table) string { return t.Name }, func(a, b schemigo.Table) bool {
		return reflect.DeepEqual(tableShape(*current, a), tableShape(desired, b))
	})
	if err != nil {
		return nil, err
	}
	var result []schemigo.Statement
	for i := range current.Tables {
		old := current.Tables[i].Name
		if next, ok := renames[old]; ok {
			source := current.Tables[i]
			target := source
			target.Name = next
			target.ForeignKeys = slices.Clone(source.ForeignKeys)
			for j := range target.ForeignKeys {
				if target.ForeignKeys[j].ReferencedTable == old {
					target.ForeignKeys[j].ReferencedTable = next
				}
			}
			sourceCondition, err := tableDefinitionCondition(*current, source)
			if err != nil {
				return nil, err
			}
			targetCondition, err := tableDefinitionCondition(*current, target)
			if err != nil {
				return nil, err
			}
			body := "IF pg_catalog.to_regclass(" + QuoteLiteral(Qualified(current.Name, old)) + ") IS NOT NULL THEN " + dependentViewGuard(current.Name, old, "") + " IF NOT (" + sourceCondition + ") THEN RAISE EXCEPTION 'rename source definition changed; replan'; END IF; IF pg_catalog.to_regclass(" + QuoteLiteral(Qualified(current.Name, next)) + ") IS NOT NULL THEN RAISE EXCEPTION 'rename target already exists'; END IF; ALTER TABLE IF EXISTS ONLY " + Qualified(current.Name, old) + " RENAME TO " + QuoteIdent(next) + "; ELSIF NOT (" + targetCondition + ") THEN RAISE EXCEPTION 'rename target definition differs or is missing; replan'; END IF;"
			result = append(result, tableDDL("DO "+QuoteLiteral("BEGIN "+body+" END"), false))
			current.Tables[i].Name = next
		}
	}
	for i := range current.Tables {
		for j := range current.Tables[i].ForeignKeys {
			fk := &current.Tables[i].ForeignKeys[j]
			if next, ok := renames[fk.ReferencedTable]; ok {
				fk.ReferencedTable = next
			}
		}
	}
	for i := range current.Triggers {
		if next, ok := renames[current.Triggers[i].Table]; ok {
			current.Triggers[i].Table = next
		}
	}
	// Resolve all column renames before comparing structured cross-table references.
	columnRenames := map[string]map[string]string{}
	for i := range current.Tables {
		old := &current.Tables[i]
		for _, next := range desired.Tables {
			if old.Name == next.Name {
				mapping, err := matchRenames(old.Columns, next.Columns, func(c schemigo.Column) string { return c.Name }, func(a, b schemigo.Column) bool { return columnShape(*current, a) == columnShape(desired, b) })
				if err != nil {
					return nil, fmt.Errorf("table %q: %w", old.Name, err)
				}
				if len(mapping) > 0 {
					raw := len(old.Checks) > 0
					for _, tr := range current.Triggers {
						raw = raw || (tr.Table == old.Name && tr.Condition != "")
					}
					for _, idx := range old.Indexes {
						raw = raw || idx.Expression != "" || idx.Where != ""
					}
					if raw {
						return nil, fmt.Errorf("table %q column rename with raw CHECK/index expressions: %w", old.Name, errors.ErrUnsupported)
					}
				}
				columnRenames[old.Name] = mapping
				for j := range old.Columns {
					if name, ok := mapping[old.Columns[j].Name]; ok {
						result = append(result, renameColumn(*current, old.Name, old.Columns[j], name))
						old.Columns[j].Name = name
					}
				}
				for j, name := range old.PrimaryKey {
					if n, ok := mapping[name]; ok {
						old.PrimaryKey[j] = n
					}
				}
				for j := range old.Indexes {
					for k, name := range old.Indexes[j].Columns {
						if n, ok := mapping[name]; ok {
							old.Indexes[j].Columns[k] = n
						}
					}
				}
			}
		}
	}
	for i := range current.Tables {
		for j := range current.Tables[i].ForeignKeys {
			fk := &current.Tables[i].ForeignKeys[j]
			for k, name := range fk.Columns {
				if n, ok := columnRenames[current.Tables[i].Name][name]; ok {
					fk.Columns[k] = n
				}
			}
			for k, name := range fk.ReferencedColumns {
				if n, ok := columnRenames[fk.ReferencedTable][name]; ok {
					fk.ReferencedColumns[k] = n
				}
			}
		}
	}
	drops, creates := diffTableObjects(*current, desired)
	result = append(result, drops...)
	oldTables := map[string]schemigo.Table{}
	wanted := map[string]bool{}
	for _, t := range current.Tables {
		oldTables[t.Name] = t
	}
	for _, next := range desired.Tables {
		wanted[next.Name] = true
		old, exists := oldTables[next.Name]
		if !exists {
			result = append(result, createTable(desired, next))
			continue
		}
		statements, err := diffColumns(*current, desired, old, next)
		if err != nil {
			return nil, err
		}
		result = append(result, statements...)
	}
	for _, old := range current.Tables {
		if !wanted[old.Name] {
			result = append(result, tableDDL("DROP TABLE IF EXISTS "+Qualified(current.Name, old.Name), true))
		}
	}
	return append(result, creates...), nil
}

func renameColumn(s schemigo.Schema, table string, old schemigo.Column, next string) schemigo.Statement {
	schema := s.Name
	exists := func(name string) string {
		return "EXISTS (SELECT FROM pg_catalog.pg_attribute WHERE attrelid=pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, table)) + ") AND attname=" + QuoteLiteral(name) + " AND attnum>0 AND NOT attisdropped)"
	}
	target := old
	target.Name = next
	body := "IF " + exists(old.Name) + " THEN " + dependentViewGuard(schema, table, old.Name) + " IF NOT (" + columnDefinitionCondition(s, table, old) + ") THEN RAISE EXCEPTION 'column rename source definition changed; replan'; END IF; IF " + exists(next) + " THEN RAISE EXCEPTION 'column rename target already exists'; END IF; ALTER TABLE IF EXISTS ONLY " + Qualified(schema, table) + " RENAME COLUMN " + QuoteIdent(old.Name) + " TO " + QuoteIdent(next) + "; ELSIF NOT (" + columnDefinitionCondition(s, table, target) + ") THEN RAISE EXCEPTION 'column rename target definition differs or is missing; replan'; END IF;"
	return tableDDL("DO "+QuoteLiteral("BEGIN "+body+" END"), false)
}

func columnDefinition(c schemigo.Column) string {
	sql := QuoteIdent(c.Name) + " " + c.Type
	if c.Default != "" {
		sql += " DEFAULT " + c.Default
	}
	if c.NotNull {
		sql += " NOT NULL"
	}
	return sql
}
func quotedColumns(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}

func createTable(s schemigo.Schema, t schemigo.Table) schemigo.Statement {
	definitions := []string{}
	for _, c := range t.Columns {
		definitions = append(definitions, columnDefinition(c))
	}
	if len(t.PrimaryKey) > 0 {
		definitions = append(definitions, "PRIMARY KEY ("+quotedColumns(t.PrimaryKey)+")")
	}
	qualified := Qualified(s.Name, t.Name)
	relation := "pg_catalog.to_regclass(" + QuoteLiteral(qualified) + ")"
	checks := []string{"(SELECT count(*) FROM pg_catalog.pg_attribute WHERE attrelid=" + relation + " AND attnum>0 AND NOT attisdropped)=" + fmt.Sprint(len(t.Columns))}
	checks = append(checks, "EXISTS (SELECT FROM pg_catalog.pg_class c WHERE c.oid="+relation+" AND c.relkind='r' AND c.relpersistence='p' AND NOT c.relispartition AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND c.reloptions IS NULL)", "NOT EXISTS (SELECT FROM pg_catalog.pg_inherits WHERE inhrelid="+relation+" OR inhparent="+relation+")")
	for i, c := range t.Columns {
		checks = append(checks, "EXISTS (SELECT FROM pg_catalog.pg_attribute a WHERE a.attrelid="+relation+" AND a.attnum="+fmt.Sprint(i+1)+" AND a.attname="+QuoteLiteral(c.Name)+" AND a.attnotnull="+fmt.Sprint(c.NotNull)+" AND "+columnTypeCondition(s, c, "a")+")")
	}
	pk := "NULL::text[]"
	if len(t.PrimaryKey) > 0 {
		pk = "ARRAY[" + enumLiterals(t.PrimaryKey) + "]::text[]"
	}
	checks = append(checks, "(SELECT array_agg(a.attname::text ORDER BY k.ord) FROM pg_catalog.pg_constraint con CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k.num WHERE con.conrelid="+relation+" AND con.contype='p') IS NOT DISTINCT FROM "+pk)
	indexNames, fkNames, checkNames := "''", "''", "''"
	for _, idx := range t.Indexes {
		indexNames += "," + QuoteLiteral(idx.Name)
	}
	for _, fk := range t.ForeignKeys {
		fkNames += "," + QuoteLiteral(fk.Name)
	}
	for _, check := range t.Checks {
		checkNames += "," + QuoteLiteral(check.Name)
	}
	triggerNames := "''"
	for _, trigger := range s.Triggers {
		if trigger.Table == t.Name {
			triggerNames += "," + QuoteLiteral(trigger.Name)
		}
	}
	checks = append(checks,
		"NOT EXISTS (SELECT FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid WHERE i.indrelid="+relation+" AND NOT i.indisprimary AND ic.relname NOT IN ("+indexNames+"))",
		"NOT EXISTS (SELECT FROM pg_catalog.pg_constraint WHERE conrelid="+relation+" AND (contype IN ('u','x') OR (contype='f' AND conname NOT IN ("+fkNames+")) OR (contype='c' AND conname NOT IN ("+checkNames+"))))",
		"NOT EXISTS (SELECT FROM pg_catalog.pg_trigger WHERE tgrelid="+relation+" AND NOT tgisinternal AND tgname NOT IN ("+triggerNames+"))")
	body := "IF " + relation + " IS NULL THEN CREATE TABLE IF NOT EXISTS " + qualified + " (" + strings.Join(definitions, ", ") + "); ELSE IF NOT (" + strings.Join(checks, " AND ") + ") THEN RAISE EXCEPTION 'existing table definition differs; replan'; END IF;\n"
	for _, c := range t.Columns {
		body += setDefaultSQL(qualified, c) + ";\n"
	}
	return tableDDL("DO "+QuoteLiteral("BEGIN "+body+"END IF; END"), false)
}

func setDefaultSQL(table string, c schemigo.Column) string {
	action := "DROP DEFAULT"
	if c.Default != "" {
		action = "SET DEFAULT " + c.Default
	}
	return "ALTER TABLE IF EXISTS ONLY " + table + " ALTER COLUMN " + QuoteIdent(c.Name) + " " + action
}

func addColumn(s schemigo.Schema, table string, c schemigo.Column) schemigo.Statement {
	qualified := Qualified(s.Name, table)
	exists := "SELECT FROM pg_catalog.pg_attribute a WHERE a.attrelid=pg_catalog.to_regclass(" + QuoteLiteral(qualified) + ") AND a.attname=" + QuoteLiteral(c.Name) + " AND a.attnum>0 AND NOT a.attisdropped"
	body := "IF EXISTS (" + exists + ") THEN IF NOT EXISTS (" + exists + " AND a.attnotnull=" + fmt.Sprint(c.NotNull) + " AND " + columnTypeCondition(s, c, "a") + ") THEN RAISE EXCEPTION 'existing column definition differs; replan'; END IF; " + setDefaultSQL(qualified, c) + "; ELSE ALTER TABLE IF EXISTS ONLY " + qualified + " ADD COLUMN IF NOT EXISTS " + columnDefinition(c) + "; END IF;"
	return tableDDL("DO "+QuoteLiteral("BEGIN "+body+" END"), false)
}

func diffColumns(current, desired schemigo.Schema, old, next schemigo.Table) ([]schemigo.Statement, error) {
	oldCols := map[string]schemigo.Column{}
	newNames := map[string]bool{}
	for _, c := range old.Columns {
		oldCols[c.Name] = c
	}
	kept := []string{}
	for _, c := range old.Columns {
		for _, n := range next.Columns {
			if c.Name == n.Name {
				kept = append(kept, c.Name)
			}
		}
	}
	for i, name := range kept {
		if i >= len(next.Columns) || next.Columns[i].Name != name {
			return nil, fmt.Errorf("table %q column reordering is unsupported: %w", next.Name, errors.ErrUnsupported)
		}
	}
	var result, dropNull []schemigo.Statement
	for _, c := range next.Columns {
		newNames[c.Name] = true
		before, exists := oldCols[c.Name]
		table := Qualified(desired.Name, next.Name)
		column := QuoteIdent(c.Name)
		if !exists {
			result = append(result, addColumn(desired, next.Name, c))
			continue
		}
		if columnTypeKey(current, before.Type) != columnTypeKey(desired, c.Type) {
			attribute := "SELECT FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class r ON r.oid=a.attrelid WHERE a.attrelid=pg_catalog.to_regclass(" + QuoteLiteral(table) + ") AND r.relkind='r' AND a.attname=" + QuoteLiteral(c.Name) + " AND a.attnum>0 AND NOT a.attisdropped AND "
			body := "IF NOT EXISTS (" + attribute + columnTypeCondition(desired, c, "a") + ") THEN\n" +
				"IF NOT EXISTS (" + attribute + columnTypeCondition(current, before, "a") + ") THEN RAISE EXCEPTION 'column type changed since planning; replan'; END IF;\n" +
				"ALTER TABLE IF EXISTS ONLY " + table + " ALTER COLUMN " + column + " DROP DEFAULT;\nALTER TABLE IF EXISTS ONLY " + table + " ALTER COLUMN " + column + " TYPE " + c.Type
			if c.Using != "" {
				body += " USING " + c.Using
			}
			body += ";\n"
			result = append(result, tableDDL("DO "+QuoteLiteral("BEGIN "+body+"END IF;\n"+setDefaultSQL(table, c)+"; END"), true))
		} else if defaultKey(current, before.Default, before.Type) != defaultKey(desired, c.Default, c.Type) {
			action := "DROP DEFAULT"
			if c.Default != "" {
				action = "SET DEFAULT " + c.Default
			}
			result = append(result, tableDDL("ALTER TABLE IF EXISTS ONLY "+table+" ALTER COLUMN "+column+" "+action, false))
		}
		if before.NotNull != c.NotNull {
			action := "DROP NOT NULL"
			if c.NotNull {
				action = "SET NOT NULL"
			}
			stmt := tableDDL("ALTER TABLE IF EXISTS ONLY "+table+" ALTER COLUMN "+column+" "+action, false)
			if c.NotNull {
				result = append(result, stmt)
			} else {
				dropNull = append(dropNull, stmt)
			}
		}
	}
	for _, c := range old.Columns {
		if !newNames[c.Name] {
			result = append(result, dropColumn(current.Name, old.Name, c.Name, old.PrimaryKey))
		}
	}
	if !slices.Equal(old.PrimaryKey, next.PrimaryKey) {
		result = append(result, primaryKeyChange(desired.Name, next))
	}
	result = append(result, dropNull...)
	return result, nil
}

func dropColumn(schema, table, column string, primaryKey []string) schemigo.Statement {
	qualified := Qualified(schema, table)
	allowed := "con.contype='n'"
	if len(primaryKey) > 0 {
		allowed = "(con.contype='n' OR (con.contype='p' AND (SELECT array_agg(a.attname::text ORDER BY k.ord) FROM unnest(con.conkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k.num)=ARRAY[" + enumLiterals(primaryKey) + "]::text[]))"
	}
	body := "IF EXISTS (SELECT FROM pg_catalog.pg_inherits WHERE inhrelid=pg_catalog.to_regclass(" + QuoteLiteral(qualified) + ") OR inhparent=pg_catalog.to_regclass(" + QuoteLiteral(qualified) + ")) OR EXISTS (SELECT FROM pg_catalog.pg_depend d WHERE d.refclassid='pg_catalog.pg_class'::regclass AND d.refobjid=pg_catalog.to_regclass(" + QuoteLiteral(qualified) + ") AND d.refobjsubid=(SELECT attnum FROM pg_catalog.pg_attribute WHERE attrelid=d.refobjid AND attname=" + QuoteLiteral(column) + " AND NOT attisdropped) " +
		"AND NOT (d.classid='pg_catalog.pg_attrdef'::regclass AND EXISTS (SELECT FROM pg_catalog.pg_attrdef ad WHERE ad.oid=d.objid AND ad.adrelid=d.refobjid AND ad.adnum=d.refobjsubid)) " +
		"AND NOT (d.classid='pg_catalog.pg_constraint'::regclass AND EXISTS (SELECT FROM pg_catalog.pg_constraint con WHERE con.oid=d.objid AND " + allowed + "))) THEN RAISE EXCEPTION 'column has dependent objects; removal refused'; END IF;\n" +
		"ALTER TABLE IF EXISTS ONLY " + qualified + " DROP COLUMN IF EXISTS " + QuoteIdent(column) + ";"
	return tableDDL("DO "+QuoteLiteral("BEGIN "+body+" END"), true)
}

func primaryKeyChange(schema string, t schemigo.Table) schemigo.Statement {
	target := "NULL::text[]"
	if len(t.PrimaryKey) > 0 {
		target = "ARRAY[" + enumLiterals(t.PrimaryKey) + "]::text[]"
	}
	body := "DECLARE constraint_name text; columns text[]; BEGIN\nSELECT con.conname, array_agg(a.attname::text ORDER BY k.ord) INTO constraint_name, columns FROM pg_catalog.pg_constraint con CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k.num WHERE con.conrelid=pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, t.Name)) + ") AND con.contype='p' GROUP BY con.conname;\n" +
		"IF columns IS NOT DISTINCT FROM " + target + " THEN RETURN; END IF;\nIF constraint_name IS NOT NULL THEN EXECUTE pg_catalog.format('ALTER TABLE ONLY %I.%I DROP CONSTRAINT %I', " + QuoteLiteral(schema) + ", " + QuoteLiteral(t.Name) + ", constraint_name); END IF;\n"
	if len(t.PrimaryKey) > 0 {
		body += "IF constraint_name IS NOT NULL THEN EXECUTE pg_catalog.format('ALTER TABLE ONLY %I.%I ADD CONSTRAINT %I PRIMARY KEY (%s)', " + QuoteLiteral(schema) + ", " + QuoteLiteral(t.Name) + ", constraint_name, " + QuoteLiteral(quotedColumns(t.PrimaryKey)) + "); ELSE ALTER TABLE IF EXISTS ONLY " + Qualified(schema, t.Name) + " ADD PRIMARY KEY (" + quotedColumns(t.PrimaryKey) + "); END IF;\n"
	}
	return tableDDL("DO "+QuoteLiteral(body+"END"), true)
}
