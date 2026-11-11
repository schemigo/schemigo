package postgres

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/schemigo/schemigo"
)

func sameIndex(a, b schemigo.Index) bool {
	a.Expression, b.Expression = expressionKey(a.Expression), expressionKey(b.Expression)
	a.Where, b.Where = expressionKey(a.Where), expressionKey(b.Where)
	return reflect.DeepEqual(a, b)
}

func indexCondition(schema, table string, idx schemigo.Index) string {
	keys := "(SELECT string_agg(pg_catalog.pg_get_indexdef(i.indexrelid, k, false), ', ' ORDER BY k) FROM generate_series(1,i.indnkeyatts) k)"
	definition := keys + " ~ " + QuoteLiteral(expressionPattern(idx.Expression))
	if len(idx.Columns) > 0 {
		definition = "i.indexprs IS NULL AND (SELECT array_agg(a.attname::text ORDER BY k.ord) FROM unnest(i.indkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.num)=ARRAY[" + enumLiterals(idx.Columns) + "]::text[]"
		definition += " AND NOT EXISTS (SELECT FROM unnest(i.indkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.num WHERE pg_catalog.pg_get_indexdef(i.indexrelid,k.ord::int,false)<>pg_catalog.quote_ident(a.attname))"
	}
	return "EXISTS (SELECT FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid JOIN pg_catalog.pg_am am ON am.oid=ic.relam WHERE ic.oid=pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, idx.Name)) + ") AND i.indrelid=pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, table)) + ") AND NOT i.indisprimary AND i.indisvalid AND i.indisready AND i.indislive AND NOT i.indisclustered AND NOT i.indnullsnotdistinct AND i.indnatts=i.indnkeyatts AND ic.reloptions IS NULL AND ic.reltablespace=0 AND i.indisunique=" + fmt.Sprint(idx.Unique) + " AND am.amname=" + QuoteLiteral(idx.Method) + " AND " + definition + " AND COALESCE(pg_catalog.pg_get_expr(i.indpred,i.indrelid),'') ~ " + QuoteLiteral(expressionPattern(idx.Where)) + " AND NOT EXISTS (SELECT FROM pg_catalog.pg_constraint con WHERE con.conindid=i.indexrelid AND con.contype IN ('p','u','x')))"
}

func createIndex(schema, table string, idx schemigo.Index) schemigo.Statement {
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	keys := idx.Expression
	if len(idx.Columns) > 0 {
		keys = quotedColumns(idx.Columns)
	}
	sql := "CREATE " + unique + "INDEX IF NOT EXISTS " + QuoteIdent(idx.Name) + " ON " + Qualified(schema, table) + " USING " + QuoteIdent(idx.Method) + " (" + keys + ")"
	if idx.Where != "" {
		sql += " WHERE " + idx.Where
	}
	body := "BEGIN IF pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, idx.Name)) + ") IS NULL THEN " + sql + "; ELSIF NOT (" + indexCondition(schema, table, idx) + ") THEN RAISE EXCEPTION 'existing index definition differs; replan'; END IF; END"
	return tableDDL("DO "+QuoteLiteral(body), false)
}

func dropIndex(schema, table string, idx schemigo.Index, targetCondition string) schemigo.Statement {
	body := "BEGIN "
	if targetCondition != "" {
		body += "IF " + targetCondition + " THEN RETURN; END IF; "
	}
	body += "IF pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, idx.Name)) + ") IS NOT NULL THEN IF NOT (" + indexCondition(schema, table, idx) + ") THEN RAISE EXCEPTION 'index definition changed since planning; replan'; END IF; DROP INDEX IF EXISTS " + Qualified(schema, idx.Name) + "; END IF; END"
	return tableDDL("DO "+QuoteLiteral(body), true)
}

func validateTableObjects(s schemigo.Schema) error {
	relations := map[string]bool{"schema_migrations": true}
	tables := map[string]schemigo.Table{}
	for _, t := range s.Tables {
		relations[t.Name] = true
		tables[t.Name] = t
	}
	for _, v := range s.Views {
		relations[v.Name] = true
	}
	for _, t := range s.Tables {
		for _, idx := range t.Indexes {
			if err := postgresName(idx.Name); err != nil {
				return err
			}
			if err := postgresName(idx.Method); err != nil {
				return err
			}
			if relations[idx.Name] {
				return fmt.Errorf("index %q conflicts with another relation in schema %q", idx.Name, s.Name)
			}
			relations[idx.Name] = true
			if idx.Type != "" || idx.Granularity != 0 {
				return fmt.Errorf("PostgreSQL index %q has ClickHouse options", idx.Name)
			}
		}
		for _, c := range tableConstraints(s.Name, t) {
			if err := postgresName(c.name); err != nil {
				return err
			}
		}
		for _, fk := range t.ForeignKeys {
			for _, action := range []string{fk.OnDelete, fk.OnUpdate} {
				switch action {
				case "NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT":
				default:
					return fmt.Errorf("foreign key %q: invalid action %q", fk.Name, action)
				}
			}
			ref := tables[fk.ReferencedTable]
			sameColumns := func(columns []string) bool {
				a, b := slices.Clone(columns), slices.Clone(fk.ReferencedColumns)
				slices.Sort(a)
				slices.Sort(b)
				return slices.Equal(a, b)
			}
			unique := sameColumns(ref.PrimaryKey)
			for _, idx := range ref.Indexes {
				unique = unique || idx.Unique && idx.Where == "" && sameColumns(idx.Columns)
			}
			if !unique {
				return fmt.Errorf("foreign key %q: referenced columns require a primary key or a non-partial unique index", fk.Name)
			}
		}
	}
	return nil
}
