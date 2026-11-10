package postgres

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/schemigo/schemigo"
)

var builtinType = regexp.MustCompile(`(?i)^(smallint|int2|integer|int4|int|bigint|int8|boolean|bool|text|varchar|character varying|character|char|numeric|decimal|real|float4|float8|double precision|date|uuid|bytea|json|jsonb)(\s*\([0-9,\s+\-]+\))?((?:\s*\[[0-9]*\])*)$`)
var timestampType = regexp.MustCompile(`(?i)^(timestamp|timestamptz|time|timetz)(\s*\([0-9\s]+\))?(?:\s+(with|without)\s+time\s+zone)?((?:\s*\[[0-9]*\])*)$`)
var arraySuffix = regexp.MustCompile(`(?:\s*\[[0-9]*\])+$`)
var typeModifier = regexp.MustCompile(`\([0-9,+\-]+\)`)
var unquotedTypeName = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)

func nativeTypeKey(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if p := timestampType.FindStringSubmatch(raw); p != nil {
		base := strings.ToLower(p[1])
		zone := strings.ToLower(p[3])
		modifier := strings.Join(strings.Fields(p[2]), "")
		array := ""
		if p[4] != "" {
			array = "[]"
		}
		if strings.HasSuffix(base, "tz") {
			if zone != "" {
				return "", false
			}
			base = strings.TrimSuffix(base, "tz")
			zone = "with"
		}
		if zone == "" {
			zone = "without"
		}
		return base + modifier + " " + zone + " time zone" + array, true
	}
	if p := builtinType.FindStringSubmatch(raw); p != nil {
		base := strings.ToLower(p[1])
		modifier := strings.Join(strings.Fields(p[2]), "")
		array := ""
		if p[3] != "" {
			array = "[]"
		}
		switch base {
		case "int8":
			base = "bigint"
		case "int", "int4":
			base = "integer"
		case "int2":
			base = "smallint"
		case "bool":
			base = "boolean"
		case "varchar":
			base = "character varying"
		case "char":
			base = "character"
		case "decimal":
			base = "numeric"
		case "float4":
			base = "real"
		case "float8":
			base = "double precision"
		}
		if base == "character" && modifier == "" {
			modifier = "(1)"
		}
		return base + modifier + array, true
	}
	return "", false
}

func columnTypeKey(s schemigo.Schema, raw string) string {
	raw = strings.TrimSpace(raw)
	if key, ok := nativeTypeKey(raw); ok {
		return key
	}
	base := arraySuffix.ReplaceAllString(raw, "")
	array := ""
	if base != raw {
		array = "[]"
	}
	for _, e := range s.Enums {
		names := []string{QuoteIdent(e.Name), Qualified(s.Name, e.Name)}
		if unquotedTypeName.MatchString(e.Name) {
			names = append(names, e.Name, QuoteIdent(s.Name)+"."+e.Name)
			if unquotedTypeName.MatchString(s.Name) {
				names = append(names, s.Name+"."+e.Name)
			}
		}
		if unquotedTypeName.MatchString(s.Name) {
			names = append(names, s.Name+"."+QuoteIdent(e.Name))
		}
		for _, name := range names {
			if base == name {
				return Qualified(s.Name, e.Name) + array
			}
		}
	}
	// shortcut: other SQL types compare textually; extend parsing when adding new type syntax.
	return raw
}

func defaultKey(s schemigo.Schema, raw, typ string) string {
	raw = strings.TrimSpace(raw)
	target := columnTypeKey(s, typ)
	if i := strings.LastIndex(raw, "::"); i >= 0 {
		cast := columnTypeKey(s, raw[i+2:])
		same := cast == target
		if _, ok := nativeTypeKey(cast); ok {
			if _, ok := nativeTypeKey(target); ok && typeModifier.ReplaceAllString(cast, "") == cast && typeModifier.ReplaceAllString(target, "") == cast {
				same = true
			}
		}
		if same {
			raw = strings.TrimSpace(raw[:i])
		}
	}
	if strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false") || strings.EqualFold(raw, "null") {
		raw = strings.ToLower(raw)
	}
	return raw
}

func columnTypeCondition(s schemigo.Schema, c schemigo.Column, alias string) string {
	key := columnTypeKey(s, c.Type)
	if _, ok := nativeTypeKey(c.Type); ok {
		return "pg_catalog.format_type(" + alias + ".atttypid," + alias + ".atttypmod)=" + QuoteLiteral(key)
	}
	return alias + ".atttypid=pg_catalog.to_regtype(" + QuoteLiteral(key) + ") AND " + alias + ".atttypmod=-1"
}

func columnDefinitionCondition(s schemigo.Schema, table string, c schemigo.Column) string {
	relation := "pg_catalog.to_regclass(" + QuoteLiteral(Qualified(s.Name, table)) + ")"
	expression := "COALESCE((SELECT pg_catalog.pg_get_expr(ad.adbin,ad.adrelid) FROM pg_catalog.pg_attrdef ad WHERE ad.adrelid=a.attrelid AND ad.adnum=a.attnum),'')"
	defaults := []string{}
	for _, value := range []string{c.Default, defaultKey(s, c.Default, c.Type)} {
		defaults = append(defaults, expression+"="+QuoteLiteral(value), expression+"=("+QuoteLiteral(value)+" || '::' || pg_catalog.format_type(a.atttypid,-1))", expression+"=("+QuoteLiteral(value)+" || '::' || pg_catalog.format_type(a.atttypid,a.atttypmod))")
	}
	return "EXISTS (SELECT FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class r ON r.oid=a.attrelid WHERE a.attrelid=" + relation + " AND r.relkind='r' AND a.attname=" + QuoteLiteral(c.Name) + " AND a.attnum>0 AND NOT a.attisdropped AND a.attnotnull=" + fmt.Sprint(c.NotNull) + " AND " + columnTypeCondition(s, c, "a") + " AND (" + strings.Join(defaults, " OR ") + "))"
}

func dependentViewGuard(schema, table, column string) string {
	relation := "pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, table)) + ")"
	query := "SELECT FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_rewrite r ON d.classid='pg_catalog.pg_rewrite'::regclass AND r.oid=d.objid JOIN pg_catalog.pg_class v ON v.oid=r.ev_class WHERE d.refclassid='pg_catalog.pg_class'::regclass AND d.refobjid=" + relation + " AND v.relkind IN ('v','m')"
	if column != "" {
		query += " AND d.refobjsubid=(SELECT attnum FROM pg_catalog.pg_attribute WHERE attrelid=" + relation + " AND attname=" + QuoteLiteral(column) + " AND NOT attisdropped)"
	}
	return "IF EXISTS (" + query + ") THEN RAISE EXCEPTION 'dependent views require view diff; rename refused'; END IF;\n"
}

func tableDefinitionCondition(s schemigo.Schema, t schemigo.Table) (string, error) {
	relation := "pg_catalog.to_regclass(" + QuoteLiteral(Qualified(s.Name, t.Name)) + ")"
	checks := []string{"EXISTS (SELECT FROM pg_catalog.pg_class WHERE oid=" + relation + " AND relkind='r' AND relpersistence='p' AND NOT relispartition AND NOT relrowsecurity AND NOT relforcerowsecurity AND reloptions IS NULL)", "NOT EXISTS (SELECT FROM pg_catalog.pg_inherits WHERE inhrelid=" + relation + " OR inhparent=" + relation + ")", "(SELECT count(*) FROM pg_catalog.pg_attribute WHERE attrelid=" + relation + " AND attnum>0 AND NOT attisdropped)=" + fmt.Sprint(len(t.Columns))}
	for _, c := range t.Columns {
		checks = append(checks, columnDefinitionCondition(s, t.Name, c))
	}
	var names []string
	for _, c := range t.Columns {
		names = append(names, c.Name)
	}
	columnNames := "NULL::text[]"
	if len(names) > 0 {
		columnNames = "ARRAY[" + enumLiterals(names) + "]::text[]"
	}
	checks = append(checks, "(SELECT array_agg(attname::text ORDER BY attnum) FROM pg_catalog.pg_attribute WHERE attrelid="+relation+" AND attnum>0 AND NOT attisdropped) IS NOT DISTINCT FROM "+columnNames)
	target := "NULL::text[]"
	if len(t.PrimaryKey) > 0 {
		target = "ARRAY[" + enumLiterals(t.PrimaryKey) + "]::text[]"
	}
	checks = append(checks, "(SELECT array_agg(a.attname::text ORDER BY k.ord) FROM pg_catalog.pg_constraint con CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k.num WHERE con.conrelid="+relation+" AND con.contype='p') IS NOT DISTINCT FROM "+target)
	var indexes []map[string]any
	for _, idx := range t.Indexes {
		indexes = append(indexes, map[string]any{"name": idx.Name, "columns": idx.Columns, "expression": idx.Expression, "unique": idx.Unique, "method": idx.Method, "where": idx.Where})
	}
	metadata, err := json.Marshal(map[string]any{"indexes": indexes, "foreign_keys": t.ForeignKeys, "checks": t.Checks})
	if err != nil {
		return "", fmt.Errorf("table %q guard metadata: %w", t.Name, err)
	}
	query := strings.ReplaceAll(tablesQuery, "$1", QuoteLiteral(s.Name))
	checks = append(checks, "(SELECT object::jsonb - 'name' - 'columns' - 'primary_key' FROM ("+query+") snapshot(object) WHERE object->>'name'="+QuoteLiteral(t.Name)+") IS NOT DISTINCT FROM "+QuoteLiteral(string(metadata))+"::jsonb")
	return strings.Join(checks, " AND "), nil
}

func tableSchemaScope(schema string, statement schemigo.Statement) schemigo.Statement {
	if strings.HasPrefix(statement.SQL, "DROP TABLE IF EXISTS ") {
		return statement
	}
	body := "DECLARE previous_path text := pg_catalog.current_setting('search_path'); BEGIN PERFORM pg_catalog.set_config('search_path', 'pg_catalog, ' || pg_catalog.quote_ident(" + QuoteLiteral(schema) + "), true);\n" + statement.SQL + ";\nPERFORM pg_catalog.set_config('search_path', previous_path, true); END"
	statement.SQL = "DO " + QuoteLiteral(body)
	return statement
}
