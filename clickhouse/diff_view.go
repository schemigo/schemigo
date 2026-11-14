package clickhouse

import (
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

func diffViews(a, b schemigo.Schema) (schemigo.Plan, schemigo.Plan, error) {
	var err error
	a.Views, err = orderViews(a.Views)
	if err != nil {
		return schemigo.Plan{}, schemigo.Plan{}, err
	}
	b.Views, err = orderViews(b.Views)
	if err != nil {
		return schemigo.Plan{}, schemigo.Plan{}, err
	}
	var before, after schemigo.Plan
	old := map[string]schemigo.View{}
	next := map[string]schemigo.View{}
	for _, v := range a.Views {
		old[v.Name] = v
	}
	for _, v := range b.Views {
		next[v.Name] = v
	}
	for i := len(a.Views) - 1; i >= 0; i-- {
		v := a.Views[i]
		n, ok := next[v.Name]
		if !ok || v.Materialized != n.Materialized || !sameView(v, n) {
			appendSQL(&before, "DROP VIEW IF EXISTS "+Qualified(a.Name, v.Name), v.Materialized)
		}
	}
	for _, v := range b.Views {
		o, ok := old[v.Name]
		if !ok || v.Materialized != o.Materialized || !sameView(o, v) {
			kind := "VIEW"
			definition := viewClause(v)
			if ok && o.Materialized == v.Materialized {
				_, explicit := withoutSecurity(v.Definition)
				_, security := withoutSecurity(o.Definition)
				if explicit == "" && security != "" {
					definition = insertSecurity(definition, security)
				}
			}
			if v.Materialized {
				kind = "MATERIALIZED VIEW"
			}
			appendSQL(&after, "CREATE "+kind+" IF NOT EXISTS "+Qualified(b.Name, v.Name)+" "+definition, false)
		}
	}
	return before, after, nil
}

func sameView(a, b schemigo.View) bool {
	_, explicit := withoutSecurity(b.Definition)
	if explicit == "" {
		a.Definition, _ = withoutSecurity(a.Definition)
	}
	if a.Materialized && b.Materialized {
		// ClickHouse adds inferred column declarations to SHOW CREATE.
		// An explicit desired declaration remains authoritative.
		if !strings.HasPrefix(strings.TrimSpace(b.Definition), "(") {
			a.Definition = storageDefinition(a.Definition)
		}
	}
	return canonical(viewKey(a.Definition)) == canonical(viewKey(b.Definition))
}
func storageDefinition(s string) string {
	tokens := sqlTokens(s)
	if len(tokens) == 0 || tokens[0].text != "(" {
		return s
	}
	depth := 0
	for _, t := range tokens {
		if t.text == "(" {
			depth++
		}
		if t.text == ")" {
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[t.end:])
			}
		}
	}
	return s
}
func orderViews(views []schemigo.View) ([]schemigo.View, error) {
	names := make([]string, 0, len(views))
	definitions := map[string]string{}
	byName := map[string]schemigo.View{}
	for _, v := range views {
		names = append(names, v.Name)
		definitions[v.Name] = v.Definition
		byName[v.Name] = v
	}
	names, err := orderDefinitions(names, definitions)
	if err != nil {
		return nil, err
	}
	ordered := make([]schemigo.View, 0, len(names))
	for _, name := range names {
		ordered = append(ordered, byName[name])
	}
	return ordered, nil
}
func orderDefinitions(names []string, definitions map[string]string) ([]string, error) {
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var ordered []string
	var visit func(string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("cyclic ClickHouse SQL dependency involving %q", name)
		}
		visiting[name] = true
		for _, t := range sqlTokens(definitions[name]) {
			dependency := strings.Trim(t.text, "`\"")
			if dependency == name {
				continue
			}
			if _, ok := definitions[dependency]; ok {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		visiting[name] = false
		visited[name] = true
		ordered = append(ordered, name)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func viewClause(v schemigo.View) string {
	if v.Materialized {
		return v.Definition
	}
	ts := sqlTokens(v.Definition)
	if len(ts) > 0 && (strings.EqualFold(ts[0].text, "DEFINER") || strings.EqualFold(ts[0].text, "SQL")) {
		return v.Definition
	}
	return "AS " + v.Definition
}
func withoutSecurity(definition string) (string, string) {
	tokens := sqlTokens(definition)
	depth, start := 0, -1
	for _, t := range tokens {
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 0 {
			continue
		}
		if strings.EqualFold(t.text, "SELECT") || strings.EqualFold(t.text, "WITH") {
			break
		}
		if start < 0 && (strings.EqualFold(t.text, "DEFINER") || strings.EqualFold(t.text, "SQL")) {
			start = t.start
		}
		if strings.EqualFold(t.text, "AS") {
			if start >= 0 {
				prefix := strings.TrimSpace(definition[:start])
				if prefix == "" {
					return strings.TrimSpace(definition[t.end:]), strings.TrimSpace(definition[start:t.start])
				}
				return prefix + " " + strings.TrimSpace(definition[t.start:]), strings.TrimSpace(definition[start:t.start])
			}
			break
		}
	}
	return definition, ""
}
func insertSecurity(clause, security string) string {
	depth := 0
	for _, t := range sqlTokens(clause) {
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && strings.EqualFold(t.text, "AS") {
			return clause[:t.start] + security + " " + clause[t.start:]
		}
	}
	return clause
}

func viewKey(definition string) string {
	tokens := sqlTokens(definition)
	depth, start := 0, -1
	for _, t := range tokens {
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 0 {
			continue
		}
		if strings.EqualFold(t.text, "SETTINGS") {
			start = t.start
		}
		if start >= 0 && (strings.EqualFold(t.text, "AS") || strings.EqualFold(t.text, "DEFINER") || strings.EqualFold(t.text, "SQL")) {
			if canonical(definition[start:t.start]) == canonical("SETTINGS index_granularity = 8192") {
				return definition[:start] + definition[t.start:]
			}
			break
		}
	}
	return definition
}
