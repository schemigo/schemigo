package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/schemigo/schemigo"
)

// Diff builds guarded, nontransactional DDL. Engine and partition changes are
// rejected because recreating a table would destroy data. Functions are global
// to the ClickHouse server, rather than scoped to Schema.Name.
func (*Driver) Diff(ctx context.Context, _ *sql.Conn, current, desired schemigo.Schema) (schemigo.Plan, error) {
	if err := ctx.Err(); err != nil {
		return schemigo.Plan{}, err
	}
	if desired.Name == "" {
		desired.Name = current.Name
	}
	if current.Name != desired.Name {
		return schemigo.Plan{}, fmt.Errorf("ClickHouse database mismatch: %q and %q", current.Name, desired.Name)
	}
	var err error
	current, err = normalize(current)
	if err != nil {
		return schemigo.Plan{}, err
	}
	desired, err = normalize(desired)
	if err != nil {
		return schemigo.Plan{}, err
	}
	var p schemigo.Plan
	// Changed views are removed before their sources change, then restored.
	tables, err := diffTables(current, desired)
	if err != nil {
		return p, err
	}
	functions := schemigo.Plan{}
	droppedFunctions := schemigo.Plan{}
	oldFunctions := map[string]schemigo.Function{}
	newFunctions := map[string]schemigo.Function{}
	for _, f := range current.Functions {
		oldFunctions[f.Name] = f
	}
	for _, f := range desired.Functions {
		newFunctions[f.Name] = f
	}
	for _, f := range current.Functions {
		if _, ok := newFunctions[f.Name]; !ok {
			appendSQL(&droppedFunctions, "DROP FUNCTION IF EXISTS "+QuoteIdent(f.Name), true)
		}
	}
	names := make([]string, 0, len(desired.Functions))
	definitions := map[string]string{}
	for _, f := range desired.Functions {
		names = append(names, f.Name)
		definitions[f.Name] = f.Body
	}
	names, err = orderDefinitions(names, definitions)
	if err != nil {
		return p, err
	}
	for _, name := range names {
		f := newFunctions[name]
		old, ok := oldFunctions[f.Name]
		if !ok || canonical(old.Parameters) != canonical(f.Parameters) || canonical(old.Body) != canonical(f.Body) {
			appendSQL(&functions, "CREATE OR REPLACE FUNCTION "+QuoteIdent(f.Name)+" AS ("+f.Parameters+") -> "+f.Body, false)
		}
	}
	before, after, err := diffViews(current, desired)
	if err != nil {
		return p, err
	}
	p.Statements = append(p.Statements, before.Statements...)
	functionsFirst := false
	for _, kind := range desired.Order {
		if kind == schemigo.EntityFunctions {
			functionsFirst = true
			break
		}
		if kind == schemigo.EntityTables {
			break
		}
	}
	for _, table := range desired.Tables {
		for _, tok := range sqlTokens(createTable(desired.Name, table)) {
			if _, ok := newFunctions[strings.Trim(tok.text, "`\"")]; ok {
				functionsFirst = true
			}
		}
	}
	if functionsFirst {
		p.Statements = append(p.Statements, functions.Statements...)
	}
	p.Statements = append(p.Statements, tables.Statements...)
	if !functionsFirst {
		p.Statements = append(p.Statements, functions.Statements...)
	}
	p.Statements = append(p.Statements, droppedFunctions.Statements...)
	p.Statements = append(p.Statements, after.Statements...)
	return p, nil
}
func appendSQL(p *schemigo.Plan, sql string, destructive bool) {
	p.Statements = append(p.Statements, schemigo.Statement{SQL: sql, Destructive: destructive, SkipExplain: true, OutsideTransaction: true})
}
func normalize(s schemigo.Schema) (schemigo.Schema, error) {
	if err := validName(s.Name); err != nil {
		return s, err
	}
	if len(s.Extensions)+len(s.Enums)+len(s.Triggers) > 0 {
		return s, fmt.Errorf("ClickHouse does not support extensions, standalone enums or triggers")
	}
	for _, t := range s.Tables {
		if t.Name == "schema_migrations" || strings.HasPrefix(t.Name, ".inner") {
			return s, fmt.Errorf("reserved ClickHouse table %q", t.Name)
		}
		if strings.TrimSpace(t.Engine) == "" || len(t.ForeignKeys) > 0 {
			return s, fmt.Errorf("table %q: Engine is required; foreign keys are unsupported", t.Name)
		}
		for _, c := range t.Columns {
			if c.Using != "" || c.PrimaryKey || c.NotNull && strings.HasPrefix(c.Type, "Nullable(") {
				return s, fmt.Errorf("column %q: use native ClickHouse Type and Table.PrimaryKey", c.Name)
			}
		}
		for _, idx := range t.Indexes {
			if idx.Type == "" || idx.Granularity <= 0 || idx.Unique || idx.Where != "" || idx.Method != "" && idx.Method != "btree" {
				return s, fmt.Errorf("index %q: Type and positive Granularity required; unique/partial/method indexes unsupported", idx.Name)
			}
		}
	}
	names := map[string]bool{}
	for _, f := range s.Functions {
		if names[f.Name] || f.Returns != "" || f.Language != "" && !strings.EqualFold(f.Language, "sql") || strings.TrimSpace(f.Body) == "" {
			return s, fmt.Errorf("function %q: only non-overloaded SQL lambdas supported", f.Name)
		}
		names[f.Name] = true
	}
	for _, v := range s.Views {
		if strings.TrimSpace(v.Definition) == "" || v.Name == "schema_migrations" {
			return s, fmt.Errorf("view %q: invalid definition or reserved name", v.Name)
		}
	}
	normalized, err := schemigo.Normalize(s)
	if err != nil {
		return normalized, err
	}
	for i := range normalized.Tables {
		table := &normalized.Tables[i]
		if len(table.PrimaryKey) > 0 {
			columns := make([]string, len(table.PrimaryKey))
			for j, name := range table.PrimaryKey {
				columns[j] = QuoteIdent(name)
			}
			if canonical(strings.Join(columns, ", ")) == canonical(table.OrderBy) {
				table.PrimaryKey = nil
			}
		}
	}
	return normalized, nil
}

type token struct {
	text       string
	start, end int
}

// sqlTokens keeps literals intact so whitespace and quoted identifiers can be
// compared without changing string values or splitting nested expressions.
func sqlTokens(s string) []token {
	var out []token
	for i := 0; i < len(s); {
		if unicode.IsSpace(rune(s[i])) {
			i++
			continue
		}
		start := i
		switch s[i] {
		case '\'', '"', '`':
			quote := s[i]
			i++
			for i < len(s) {
				if s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == quote {
					i++
					if i < len(s) && s[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
			if i > len(s) {
				i = len(s)
			}
		default:
			if s[i] == '_' || unicode.IsLetter(rune(s[i])) || unicode.IsDigit(rune(s[i])) {
				for i < len(s) && (s[i] == '_' || unicode.IsLetter(rune(s[i])) || unicode.IsDigit(rune(s[i]))) {
					i++
				}
			} else {
				i++
			}
		}
		out = append(out, token{s[start:i], start, i})
	}
	return out
}
func canonical(s string) string {
	ts := sqlTokens(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";")))
	for len(ts) >= 2 && ts[0].text == "(" && ts[len(ts)-1].text == ")" {
		depth, wraps := 0, true
		for i, t := range ts {
			if t.text == "(" {
				depth++
			}
			if t.text == ")" {
				depth--
			}
			if depth == 0 && i != len(ts)-1 {
				wraps = false
				break
			}
		}
		if !wraps {
			break
		}
		ts = ts[1 : len(ts)-1]
	}
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		v := t.text
		if len(v) > 1 && (v[0] == '`' || v[0] == '"') {
			q := v[:1]
			v = strings.ReplaceAll(v[1:len(v)-1], q+q, q)
			v = strings.NewReplacer("\\`", "`", "\\\"", "\"", "\\\\", "\\").Replace(v)
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, "\x00")
}
func engineKey(s string) string {
	base, settings := engineParts(s)
	// ClickHouse materializes this default in MergeTree metadata.
	if canonical(settings) == canonical("SETTINGS index_granularity = 8192") {
		settings = ""
	}
	return canonical(strings.TrimSuffix(strings.TrimSpace(base), "()")) + canonical(settings)
}
