package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/schemigo/schemigo"
)

type enumColumn struct {
	Schema  string `json:"schema"`
	Table   string `json:"table"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Array   bool   `json:"array"`
	Default string `json:"default"`
}

func enumLiterals(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = QuoteLiteral(v)
	}
	return strings.Join(parts, ", ")
}

func enumValuesQuery(qualified string) string {
	return "SELECT array_agg(e.enumlabel::text ORDER BY e.enumsortorder) FROM pg_catalog.pg_enum e WHERE e.enumtypid=pg_catalog.to_regtype(" + QuoteLiteral(qualified) + ")"
}

func enumCreate(schema string, e schemigo.Enum) schemigo.Statement {
	name := Qualified(schema, e.Name)
	body := "DECLARE labels text[]; BEGIN\n" + enumValuesQuery(name) + " INTO labels;\n" +
		"IF pg_catalog.to_regtype(" + QuoteLiteral(name) + ") IS NOT NULL THEN\n" +
		"IF labels IS DISTINCT FROM ARRAY[" + enumLiterals(e.Values) + "]::text[] THEN RAISE EXCEPTION 'enum definition differs; replan'; END IF; RETURN; END IF;\n" +
		"CREATE TYPE " + name + " AS ENUM (" + enumLiterals(e.Values) + ");\nEND"
	return schemigo.Statement{SQL: "DO " + QuoteLiteral(body), SkipExplain: true}
}

func diffEnums(ctx context.Context, conn *sql.Conn, current, desired schemigo.Schema) ([]schemigo.Statement, []enumColumn, error) {
	old := map[string]schemigo.Enum{}
	wanted := map[string]bool{}
	for _, e := range current.Enums {
		old[e.Name] = e
	}
	enums := slices.Clone(desired.Enums)
	slices.SortFunc(enums, func(a, b schemigo.Enum) int { return strings.Compare(a.Name, b.Name) })
	var result []schemigo.Statement
	var columns []enumColumn
	for _, e := range enums {
		wanted[e.Name] = true
		before, exists := old[e.Name]
		if !exists {
			result = append(result, enumCreate(desired.Name, e))
			continue
		}
		if slices.Equal(before.Values, e.Values) {
			continue
		}
		kept := []string{}
		for _, v := range e.Values {
			if slices.Contains(before.Values, v) {
				kept = append(kept, v)
			}
		}
		if !slices.Equal(kept, before.Values) {
			affected, err := enumDependencies(ctx, conn, current, desired, e.Name)
			if err != nil {
				return nil, nil, err
			}
			result = append(result, enumRebuild(desired.Name, before, e, affected))
			columns = append(columns, affected...)
			continue
		}
		present := map[string]bool{}
		for _, v := range before.Values {
			present[v] = true
		}
		for i, v := range e.Values {
			if present[v] {
				continue
			}
			sql := "ALTER TYPE " + Qualified(desired.Name, e.Name) + " ADD VALUE IF NOT EXISTS " + QuoteLiteral(v)
			next := -1
			for j := i + 1; j < len(e.Values); j++ {
				if present[e.Values[j]] {
					next = j
					break
				}
			}
			if next >= 0 {
				sql += " BEFORE " + QuoteLiteral(e.Values[next])
			} else if i > 0 {
				sql += " AFTER " + QuoteLiteral(e.Values[i-1])
			}
			result = append(result, schemigo.Statement{SQL: sql, SkipExplain: true, OutsideTransaction: true})
			present[v] = true
		}
	}
	removed := slices.Clone(current.Enums)
	slices.SortFunc(removed, func(a, b schemigo.Enum) int { return strings.Compare(a.Name, b.Name) })
	for _, e := range removed {
		if !wanted[e.Name] {
			result = append(result, schemigo.Statement{SQL: "DROP TYPE IF EXISTS " + Qualified(current.Name, e.Name), Destructive: true, SkipExplain: true})
		}
	}
	return result, columns, nil
}

func enumRebuild(schema string, before, desired schemigo.Enum, columns []enumColumn) schemigo.Statement {
	name := Qualified(schema, desired.Name)
	temporary := desired.Name + "_new"
	if len(temporary) > 63 {
		hash := sha256.Sum256([]byte(name))
		temporary = fmt.Sprintf("schemigo_%x_new", hash[:16])
	}
	temp := Qualified(schema, temporary)
	body := "DECLARE labels text[]; old_path text := pg_catalog.current_setting('search_path'); old_comment text := pg_catalog.obj_description(pg_catalog.to_regtype(" + QuoteLiteral(name) + "), 'pg_type'); BEGIN\n" +
		enumValuesQuery(name) + " INTO labels;\n" +
		"IF labels IS NOT DISTINCT FROM ARRAY[" + enumLiterals(desired.Values) + "]::text[] THEN RETURN; END IF;\n" +
		"IF labels IS DISTINCT FROM ARRAY[" + enumLiterals(before.Values) + "]::text[] THEN RAISE EXCEPTION 'enum changed since planning; replan'; END IF;\n" +
		"IF pg_catalog.to_regtype(" + QuoteLiteral(temp) + ") IS NOT NULL THEN RAISE EXCEPTION 'temporary enum already exists; refusing to replace it'; END IF;\n"
	tables := []string{}
	for _, c := range columns {
		t := Qualified(schema, c.Table)
		if !slices.Contains(tables, t) {
			tables = append(tables, t)
		}
	}
	if len(tables) > 0 {
		body += "LOCK TABLE " + strings.Join(tables, ", ") + " IN ACCESS EXCLUSIVE MODE;\n"
	}
	body += "PERFORM pg_catalog.set_config('search_path', 'pg_catalog, ' || pg_catalog.quote_ident(" + QuoteLiteral(schema) + "), true);\n" +
		"CREATE TYPE " + temp + " AS ENUM (" + enumLiterals(desired.Values) + ");\n"
	body += "IF EXISTS (SELECT FROM pg_catalog.pg_type old_type JOIN pg_catalog.pg_type new_type ON new_type.oid=pg_catalog.to_regtype(" + QuoteLiteral(temp) + ") " +
		"WHERE old_type.oid=pg_catalog.to_regtype(" + QuoteLiteral(name) + ") AND (old_type.typowner<>new_type.typowner OR " +
		"COALESCE(old_type.typacl, pg_catalog.acldefault('T', old_type.typowner)) IS DISTINCT FROM COALESCE(new_type.typacl, pg_catalog.acldefault('T', new_type.typowner)))) " +
		"THEN RAISE EXCEPTION 'enum ownership or privileges would change; rebuild refused'; END IF;\n"
	for _, c := range columns {
		column := QuoteIdent(c.Name)
		table := Qualified(schema, c.Table)
		typ := temp
		if c.Array {
			typ += "[]"
		}
		body += "ALTER TABLE IF EXISTS " + table + " ALTER COLUMN " + column + " DROP DEFAULT;\n" +
			"ALTER TABLE IF EXISTS " + table + " ALTER COLUMN " + column + " TYPE " + typ + " USING (" + column + "::text::" + typ + ");\n"
	}
	body += "DROP TYPE IF EXISTS " + name + ";\nALTER TYPE " + temp + " RENAME TO " + QuoteIdent(desired.Name) + ";\n"
	for _, c := range columns {
		if c.Default != "" {
			body += "ALTER TABLE IF EXISTS " + Qualified(schema, c.Table) + " ALTER COLUMN " + QuoteIdent(c.Name) + " SET DEFAULT " + c.Default + ";\n"
		}
	}
	body += "EXECUTE pg_catalog.format('COMMENT ON TYPE %I.%I IS %L', " + QuoteLiteral(schema) + ", " + QuoteLiteral(desired.Name) + ", old_comment);\n"
	body += "PERFORM pg_catalog.set_config('search_path', old_path, true);\nEND"
	return schemigo.Statement{SQL: "DO " + QuoteLiteral(body), Destructive: true, SkipExplain: true}
}

func enumDependencies(ctx context.Context, conn *sql.Conn, current, desired schemigo.Schema, name string) ([]enumColumn, error) {
	if conn == nil {
		return nil, fmt.Errorf("enum %q rebuild requires a catalog connection", name)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("enum dependencies: %w", err)
	}
	defer tx.Rollback()
	qualified := Qualified(current.Name, name)
	columns, err := inspectJSON[enumColumn](ctx, tx, qualified, enumColumnsQuery)
	if err != nil {
		return nil, fmt.Errorf("enum %q columns: %w", name, err)
	}
	for i, c := range columns {
		found := false
		if c.Schema == desired.Name && c.Kind == "r" {
			for _, table := range desired.Tables {
				if table.Name == c.Table {
					for _, column := range table.Columns {
						if column.Name == c.Name {
							columns[i].Default = column.Default
							found = true
						}
					}
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("enum %q: unmanaged dependent column %s.%s", name, Qualified(c.Schema, c.Table), QuoteIdent(c.Name))
		}
	}
	obstacles, err := inspectJSON[string](ctx, tx, qualified, enumObstaclesQuery)
	if err != nil {
		return nil, fmt.Errorf("enum %q dependencies: %w", name, err)
	}
	if len(obstacles) > 0 {
		return nil, fmt.Errorf("enum %q has unsupported dependencies: %s", name, strings.Join(obstacles, ", "))
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("enum dependencies snapshot: %w", err)
	}
	return columns, nil
}

const enumTargetCTE = `WITH target AS (
 SELECT t.oid, t.typarray FROM pg_catalog.pg_type t
 WHERE t.oid=pg_catalog.to_regtype($1) AND t.typtype='e'
), columns AS (
 SELECT a.attrelid, a.attnum, a.atttypid, n.nspname, c.relname, c.relkind, a.attname
 FROM target t JOIN pg_catalog.pg_attribute a ON a.atttypid IN (t.oid,t.typarray)
 JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE a.attnum>0 AND NOT a.attisdropped AND c.relkind NOT IN ('i','I')
) `

const enumColumnsQuery = enumTargetCTE + `
SELECT json_build_object('schema', c.nspname, 'table', c.relname, 'kind', c.relkind,
 'name', c.attname, 'array', c.atttypid=t.typarray)
FROM columns c CROSS JOIN target t
ORDER BY c.nspname,c.relname,c.attnum`

const enumObstaclesQuery = enumTargetCTE + `
SELECT to_json(description) FROM (
 SELECT pg_catalog.pg_describe_object(d.classid,d.objid,d.objsubid) AS description
 FROM target t JOIN pg_catalog.pg_depend d ON d.refclassid='pg_catalog.pg_type'::regclass
 AND d.refobjid IN (t.oid,t.typarray)
 WHERE NOT (d.classid='pg_catalog.pg_type'::regclass AND d.objid=t.typarray AND d.deptype='i')
 AND NOT (d.classid='pg_catalog.pg_class'::regclass AND EXISTS (
  SELECT FROM columns c WHERE c.attrelid=d.objid AND c.attnum=d.objsubid))
 AND NOT (d.classid='pg_catalog.pg_attrdef'::regclass AND EXISTS (
  SELECT FROM pg_catalog.pg_attrdef ad JOIN columns c ON c.attrelid=ad.adrelid AND c.attnum=ad.adnum WHERE ad.oid=d.objid))
 UNION
 SELECT pg_catalog.pg_describe_object(d.classid,d.objid,d.objsubid)
 FROM columns c JOIN pg_catalog.pg_depend d ON d.refclassid='pg_catalog.pg_class'::regclass
 AND d.refobjid=c.attrelid AND d.refobjsubid=c.attnum
 WHERE d.classid NOT IN ('pg_catalog.pg_attrdef'::regclass)
 AND NOT (d.classid='pg_catalog.pg_class'::regclass AND EXISTS (
  SELECT FROM pg_catalog.pg_index i WHERE i.indexrelid=d.objid AND i.indexprs IS NULL AND i.indpred IS NULL))
 AND NOT (d.classid='pg_catalog.pg_constraint'::regclass AND EXISTS (
  SELECT FROM pg_catalog.pg_constraint con WHERE con.oid=d.objid AND con.contype IN ('p','u','c','n')))
 UNION
 SELECT 'extension membership: ' || e.extname
 FROM target t JOIN pg_catalog.pg_depend d ON d.classid='pg_catalog.pg_type'::regclass
 AND d.objid=t.oid AND d.deptype='e'
 JOIN pg_catalog.pg_extension e ON e.oid=d.refobjid
) obstacles ORDER BY description`
