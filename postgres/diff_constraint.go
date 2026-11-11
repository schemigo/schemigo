package postgres

import (
	"fmt"
	"slices"

	"github.com/schemigo/schemigo"
)

type tableConstraint struct {
	name, definition, condition string
	foreign                     bool
}

func tableConstraints(schema string, t schemigo.Table) []tableConstraint {
	var result []tableConstraint
	columns := func(table, key string) string {
		return "(SELECT array_agg(a.attname::text ORDER BY k.ord) FROM unnest(con." + key + ") WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=con." + table + " AND a.attnum=k.num)"
	}
	actions := map[string]string{"NO ACTION": "a", "RESTRICT": "r", "CASCADE": "c", "SET NULL": "n", "SET DEFAULT": "d"}
	for _, fk := range t.ForeignKeys {
		definition := "FOREIGN KEY (" + quotedColumns(fk.Columns) + ") REFERENCES " + Qualified(schema, fk.ReferencedTable) + " (" + quotedColumns(fk.ReferencedColumns) + ") ON DELETE " + fk.OnDelete + " ON UPDATE " + fk.OnUpdate
		condition := "con.contype='f' AND con.confmatchtype='s' AND con.confdelsetcols IS NULL AND con.confrelid=pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, fk.ReferencedTable)) + ") AND " + columns("conrelid", "conkey") + "=ARRAY[" + enumLiterals(fk.Columns) + "]::text[] AND " + columns("confrelid", "confkey") + "=ARRAY[" + enumLiterals(fk.ReferencedColumns) + "]::text[] AND con.confdeltype=" + QuoteLiteral(actions[fk.OnDelete]) + " AND con.confupdtype=" + QuoteLiteral(actions[fk.OnUpdate])
		result = append(result, tableConstraint{name: fk.Name, definition: definition, condition: condition, foreign: true})
	}
	for _, check := range t.Checks {
		result = append(result, tableConstraint{name: check.Name, definition: "CHECK (" + check.Expression + ")", condition: "con.contype='c' AND NOT con.connoinherit AND pg_catalog.pg_get_expr(con.conbin,con.conrelid) ~ " + QuoteLiteral(expressionPattern(check.Expression))})
	}
	return result
}

func constraintCondition(schema, table string, c tableConstraint) string {
	return "EXISTS (SELECT FROM pg_catalog.pg_constraint con WHERE con.conrelid=pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, table)) + ") AND con.conname=" + QuoteLiteral(c.name) + " AND NOT con.condeferrable AND NOT con.condeferred AND con.convalidated AND con.conislocal AND con.coninhcount=0 AND COALESCE((pg_catalog.to_jsonb(con)->>'conenforced')::boolean,true) AND " + c.condition + ")"
}

func createConstraint(schema, table string, c tableConstraint) schemigo.Statement {
	alter := "ALTER TABLE IF EXISTS ONLY " + Qualified(schema, table)
	body := "BEGIN IF EXISTS (SELECT FROM pg_catalog.pg_constraint WHERE conrelid=pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, table)) + ") AND conname=" + QuoteLiteral(c.name) + ") THEN IF NOT (" + constraintCondition(schema, table, c) + ") THEN RAISE EXCEPTION 'existing constraint definition differs; replan'; END IF; ELSE " + alter + " ADD CONSTRAINT " + QuoteIdent(c.name) + " " + c.definition + " NOT VALID; " + alter + " VALIDATE CONSTRAINT " + QuoteIdent(c.name) + "; END IF; END"
	return tableDDL("DO "+QuoteLiteral(body), false)
}

func dropConstraint(schema, table string, c tableConstraint, targetCondition string) schemigo.Statement {
	body := "BEGIN "
	if targetCondition != "" {
		body += "IF " + targetCondition + " THEN RETURN; END IF; "
	}
	body += "IF EXISTS (SELECT FROM pg_catalog.pg_constraint WHERE conrelid=pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, table)) + ") AND conname=" + QuoteLiteral(c.name) + ") THEN IF NOT (" + constraintCondition(schema, table, c) + ") THEN RAISE EXCEPTION 'constraint definition changed since planning; replan'; END IF; ALTER TABLE IF EXISTS ONLY " + Qualified(schema, table) + " DROP CONSTRAINT IF EXISTS " + QuoteIdent(c.name) + "; END IF; END"
	return tableDDL("DO "+QuoteLiteral(body), true)
}

func columnsChanged(current, desired schemigo.Schema, old, next schemigo.Table) bool {
	for _, col := range old.Columns {
		found := false
		for _, target := range next.Columns {
			if col.Name == target.Name {
				found = columnTypeKey(current, col.Type) == columnTypeKey(desired, target.Type)
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func diffTableObjects(current, desired schemigo.Schema) (drops, creates []schemigo.Statement) {
	oldTables, newTables := map[string]schemigo.Table{}, map[string]schemigo.Table{}
	newIndexes := map[string]string{}
	for _, t := range current.Tables {
		oldTables[t.Name] = t
	}
	for _, t := range desired.Tables {
		newTables[t.Name] = t
		for _, idx := range t.Indexes {
			newIndexes[idx.Name] = "(" + indexCondition(desired.Name, t.Name, idx) + ") AND (" + tableStructureCondition(desired, t) + ")"
		}
	}
	var fkDrops, otherDrops, fkCreates []schemigo.Statement
	for _, table := range current.Tables {
		next, kept := newTables[table.Name]
		changed := columnsChanged(current, desired, table, next)
		for _, old := range tableConstraints(current.Name, table) {
			var target *tableConstraint
			for _, c := range tableConstraints(desired.Name, next) {
				if c.name == old.name {
					target = &c
				}
			}
			force := changed
			if old.foreign {
				force = fkNeedsRebuild(current, desired, table, next, old.name)
			}
			if target != nil && target.condition == old.condition && !force {
				continue
			}
			if !kept && !old.foreign {
				continue
			}
			targetCondition := ""
			if target != nil {
				targetCondition = constraintCondition(desired.Name, next.Name, *target)
				if force {
					targetCondition += " AND (" + tableStructureCondition(desired, next) + ")"
					for _, fk := range next.ForeignKeys {
						if fk.Name == target.name {
							targetCondition += " AND (" + tableStructureCondition(desired, newTables[fk.ReferencedTable]) + ")"
							for _, idx := range newTables[fk.ReferencedTable].Indexes {
								targetCondition += " AND (" + indexCondition(desired.Name, fk.ReferencedTable, idx) + ")"
							}
							for _, idx := range oldTables[fk.ReferencedTable].Indexes {
								if !slices.ContainsFunc(newTables[fk.ReferencedTable].Indexes, func(i schemigo.Index) bool { return i.Name == idx.Name }) {
									targetCondition += " AND pg_catalog.to_regclass(" + QuoteLiteral(Qualified(desired.Name, idx.Name)) + ") IS NULL"
								}
							}
						}
					}
				}
			}
			stmt := dropConstraint(current.Name, table.Name, old, targetCondition)
			if old.foreign {
				fkDrops = append(fkDrops, stmt)
			} else {
				otherDrops = append(otherDrops, stmt)
			}
		}
		if !kept {
			continue
		}
		for _, idx := range table.Indexes {
			if !changed && slices.ContainsFunc(next.Indexes, func(target schemigo.Index) bool { return sameIndex(idx, target) }) {
				continue
			}
			targetCondition := newIndexes[idx.Name]
			otherDrops = append(otherDrops, dropIndex(current.Name, table.Name, idx, targetCondition))
		}
	}
	for _, table := range desired.Tables {
		old := oldTables[table.Name]
		changed := columnsChanged(current, desired, old, table)
		for _, idx := range table.Indexes {
			if !changed && slices.ContainsFunc(old.Indexes, func(before schemigo.Index) bool { return sameIndex(before, idx) }) {
				continue
			}
			creates = append(creates, createIndex(desired.Name, table.Name, idx))
		}
		for _, c := range tableConstraints(desired.Name, table) {
			force := changed
			if c.foreign {
				force = fkNeedsRebuild(current, desired, old, table, c.name)
			}
			if !force && slices.ContainsFunc(tableConstraints(current.Name, old), func(before tableConstraint) bool { return before.name == c.name && before.condition == c.condition }) {
				continue
			}
			stmt := createConstraint(desired.Name, table.Name, c)
			if c.foreign {
				fkCreates = append(fkCreates, stmt)
			} else {
				creates = append(creates, stmt)
			}
		}
	}
	drops = append(fkDrops, otherDrops...)
	creates = append(creates, fkCreates...)
	return drops, creates
}

func tableStructureCondition(s schemigo.Schema, t schemigo.Table) string {
	relation := "pg_catalog.to_regclass(" + QuoteLiteral(Qualified(s.Name, t.Name)) + ")"
	result := "(SELECT count(*) FROM pg_catalog.pg_attribute WHERE attrelid=" + relation + " AND attnum>0 AND NOT attisdropped)=" + fmt.Sprint(len(t.Columns))
	for _, c := range t.Columns {
		result += " AND EXISTS (SELECT FROM pg_catalog.pg_attribute a WHERE a.attrelid=" + relation + " AND a.attname=" + QuoteLiteral(c.Name) + " AND a.attnum>0 AND NOT a.attisdropped AND " + columnTypeCondition(s, c, "a") + ")"
	}
	pk := "NULL::text[]"
	if len(t.PrimaryKey) > 0 {
		pk = "ARRAY[" + enumLiterals(t.PrimaryKey) + "]::text[]"
	}
	return result + " AND (SELECT array_agg(a.attname::text ORDER BY k.ord) FROM pg_catalog.pg_constraint con CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k.num WHERE con.conrelid=" + relation + " AND con.contype='p') IS NOT DISTINCT FROM " + pk
}

func fkNeedsRebuild(current, desired schemigo.Schema, old, next schemigo.Table, name string) bool {
	if columnsChanged(current, desired, old, next) {
		return true
	}
	for _, fk := range old.ForeignKeys {
		if fk.Name != name {
			continue
		}
		var before, after schemigo.Table
		for _, t := range current.Tables {
			if t.Name == fk.ReferencedTable {
				before = t
			}
		}
		for _, t := range desired.Tables {
			if t.Name == fk.ReferencedTable {
				after = t
			}
		}
		indexesChanged := len(before.Indexes) != len(after.Indexes)
		if !indexesChanged {
			for i := range before.Indexes {
				indexesChanged = indexesChanged || !sameIndex(before.Indexes[i], after.Indexes[i])
			}
		}
		return columnsChanged(current, desired, before, after) || !slices.Equal(before.PrimaryKey, after.PrimaryKey) || indexesChanged
	}
	return false
}
