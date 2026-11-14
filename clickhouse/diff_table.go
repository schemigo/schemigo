package clickhouse

import (
	"fmt"
	"github.com/schemigo/schemigo"
	"strings"
)

func columnSQL(c schemigo.Column) string {
	s := QuoteIdent(c.Name) + " " + c.Type
	if c.Default != "" {
		s += " DEFAULT " + c.Default
	}
	return s
}
func indexSQL(idx schemigo.Index) string {
	expression := idx.Expression
	if len(idx.Columns) > 0 {
		names := make([]string, len(idx.Columns))
		for i, n := range idx.Columns {
			names[i] = QuoteIdent(n)
		}
		expression = strings.Join(names, ", ")
		if len(names) > 1 {
			expression = "(" + expression + ")"
		}
	}
	return fmt.Sprintf("INDEX %s %s TYPE %s GRANULARITY %d", QuoteIdent(idx.Name), expression, idx.Type, idx.Granularity)
}
func primarySQL(t schemigo.Table) string {
	if len(t.PrimaryKey) == 0 {
		return ""
	}
	names := make([]string, len(t.PrimaryKey))
	for i, n := range t.PrimaryKey {
		names[i] = QuoteIdent(n)
	}
	return " PRIMARY KEY (" + strings.Join(names, ", ") + ")"
}
func createTable(database string, t schemigo.Table) string {
	parts := make([]string, 0, len(t.Columns)+len(t.Indexes)+len(t.Checks))
	for _, c := range t.Columns {
		parts = append(parts, columnSQL(c))
	}
	for _, idx := range t.Indexes {
		parts = append(parts, indexSQL(idx))
	}
	for _, c := range t.Checks {
		parts = append(parts, "CONSTRAINT "+QuoteIdent(c.Name)+" CHECK "+c.Expression)
	}
	engine, settings := engineParts(t.Engine)
	s := "CREATE TABLE IF NOT EXISTS " + Qualified(database, t.Name) + " (" + strings.Join(parts, ", ") + ") ENGINE = " + engine
	if t.PartitionBy != "" {
		s += " PARTITION BY " + t.PartitionBy
	}
	s += primarySQL(t)
	if t.OrderBy != "" {
		s += " ORDER BY " + t.OrderBy
	}
	if settings != "" {
		s += " " + settings
	}
	return s
}
func engineParts(engine string) (string, string) {
	depth := 0
	for _, t := range sqlTokens(engine) {
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && strings.EqualFold(t.text, "SETTINGS") {
			return strings.TrimSpace(engine[:t.start]), strings.TrimSpace(engine[t.start:])
		}
	}
	return engine, ""
}
func diffTables(a, b schemigo.Schema) (schemigo.Plan, error) {
	var p schemigo.Plan
	old := map[string]schemigo.Table{}
	next := map[string]schemigo.Table{}
	for _, t := range a.Tables {
		old[t.Name] = t
	}
	for _, t := range b.Tables {
		next[t.Name] = t
	}
	for _, t := range a.Tables {
		if _, ok := next[t.Name]; !ok {
			appendSQL(&p, "DROP TABLE IF EXISTS "+Qualified(a.Name, t.Name), true)
		}
	}
	for _, t := range b.Tables {
		prev, ok := old[t.Name]
		if !ok {
			appendSQL(&p, createTable(b.Name, t), false)
			continue
		}
		if engineKey(prev.Engine) != engineKey(t.Engine) || canonical(prev.PartitionBy) != canonical(t.PartitionBy) || canonical(primarySQL(prev)) != canonical(primarySQL(t)) {
			return p, fmt.Errorf("table %q: changing engine, partition or primary key requires a manual data migration", t.Name)
		}
		prefix := "ALTER TABLE " + Qualified(b.Name, t.Name) + " "
		oldColumns := map[string]schemigo.Column{}
		newColumns := map[string]schemigo.Column{}
		for _, c := range prev.Columns {
			oldColumns[c.Name] = c
		}
		for _, c := range t.Columns {
			newColumns[c.Name] = c
		}
		// Drop dependent indexes and constraints before removing or modifying columns.
		oldIdx := map[string]string{}
		newIdx := map[string]string{}
		for _, idx := range prev.Indexes {
			oldIdx[idx.Name] = indexSQL(idx)
		}
		for _, idx := range t.Indexes {
			newIdx[idx.Name] = indexSQL(idx)
		}
		for _, idx := range prev.Indexes {
			if canonical(oldIdx[idx.Name]) != canonical(newIdx[idx.Name]) {
				appendSQL(&p, prefix+"DROP INDEX IF EXISTS "+QuoteIdent(idx.Name), false)
			}
		}
		oldChecks := map[string]string{}
		newChecks := map[string]string{}
		for _, c := range prev.Checks {
			oldChecks[c.Name] = c.Expression
		}
		for _, c := range t.Checks {
			newChecks[c.Name] = c.Expression
		}
		for _, c := range prev.Checks {
			if canonical(c.Expression) != canonical(newChecks[c.Name]) {
				appendSQL(&p, prefix+"DROP CONSTRAINT IF EXISTS "+QuoteIdent(c.Name), false)
			}
		}
		for _, c := range t.Columns {
			previous, ok := oldColumns[c.Name]
			if !ok {
				appendSQL(&p, prefix+"ADD COLUMN IF NOT EXISTS "+columnSQL(c), false)
			} else if canonical(previous.Type) != canonical(c.Type) || canonical(previous.Default) != canonical(c.Default) {
				appendSQL(&p, prefix+"MODIFY COLUMN IF EXISTS "+columnSQL(c), canonical(previous.Type) != canonical(c.Type))
				if previous.Default != "" && c.Default == "" {
					appendSQL(&p, prefix+"MODIFY COLUMN IF EXISTS "+QuoteIdent(c.Name)+" REMOVE DEFAULT", false)
				}
			}
		}
		for _, c := range prev.Columns {
			if _, ok := newColumns[c.Name]; !ok {
				appendSQL(&p, prefix+"DROP COLUMN IF EXISTS "+QuoteIdent(c.Name), true)
			}
		}
		if canonical(prev.OrderBy) != canonical(t.OrderBy) {
			if t.OrderBy == "" {
				return p, fmt.Errorf("table %q: removing ORDER BY is unsupported", t.Name)
			}
			appendSQL(&p, prefix+"MODIFY ORDER BY "+t.OrderBy, false)
		}
		for _, idx := range t.Indexes {
			if canonical(oldIdx[idx.Name]) != canonical(newIdx[idx.Name]) {
				appendSQL(&p, prefix+"ADD "+strings.Replace(indexSQL(idx), "INDEX ", "INDEX IF NOT EXISTS ", 1), false)
			}
		}
		for _, c := range t.Checks {
			if canonical(c.Expression) != canonical(oldChecks[c.Name]) {
				appendSQL(&p, prefix+"ADD CONSTRAINT IF NOT EXISTS "+QuoteIdent(c.Name)+" CHECK "+c.Expression, false)
			}
		}
	}
	return p, nil
}
