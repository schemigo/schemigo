package postgres

import (
	"fmt"
	"slices"
	"strings"

	"github.com/schemigo/schemigo"
)

type functionChange struct {
	old, next *schemigo.Function
	identity  string
}

func sqlDefinitionKey(text string) string {
	tokens := expressionTokens(text)
	for len(tokens) > 0 && tokens[len(tokens)-1] == ";" {
		tokens = tokens[:len(tokens)-1]
	}
	for i, t := range tokens {
		if len(t) > 0 && t[0] != '\'' && t[0] != '"' && t[0] != '$' {
			tokens[i] = strings.ToLower(t)
		}
	}
	return strings.Join(tokens, "\x00")
}

func dollarQuote(body string) string {
	tag := "$schemigo$"
	for i := 1; strings.Contains(body, tag); i++ {
		tag = fmt.Sprintf("$schemigo_%d$", i)
	}
	return tag + body + tag
}

// functionArguments separates input identity from argument names and defaults.
func functionArguments(s schemigo.Schema, raw string) (identity, declaration string, err error) {
	tokens := expressionTokens(raw)
	var parts [][]string
	start, depth := 0, 0
	for i, t := range tokens {
		switch t {
		case "(", "[":
			depth++
		case ")", "]":
			depth--
		case ",":
			if depth == 0 {
				parts = append(parts, tokens[start:i])
				start = i + 1
			}
		}
		if depth < 0 {
			return "", "", fmt.Errorf("unbalanced function parameters")
		}
	}
	if depth != 0 {
		return "", "", fmt.Errorf("unbalanced function parameters")
	}
	if len(tokens) > 0 {
		parts = append(parts, tokens[start:])
	}
	var inputs, decls []string
	for _, part := range parts {
		if len(part) == 0 {
			return "", "", fmt.Errorf("empty function parameter")
		}
		mode := "IN"
		switch strings.ToUpper(part[0]) {
		case "IN", "OUT", "INOUT", "VARIADIC":
			mode = strings.ToUpper(part[0])
			part = part[1:]
		}
		var defaultValue string
		depth = 0
		for i, t := range part {
			if depth == 0 && (strings.EqualFold(t, "DEFAULT") || t == "=") {
				defaultValue = strings.Join(part[i+1:], " ")
				part = part[:i]
				break
			}
			switch t {
			case "(", "[":
				depth++
			case ")", "]":
				depth--
			}
		}
		if len(part) == 0 {
			return "", "", fmt.Errorf("missing function parameter type")
		}
		name := ""
		joined := strings.Join(part, " ")
		_, native := nativeTypeKey(joined)
		if !native && len(part) > 1 && part[1] != "." && part[1] != "[" && part[1] != "(" {
			name = part[0]
			part = part[1:]
		}
		typ := strings.Join(part, " ")
		typ = strings.ReplaceAll(strings.ReplaceAll(typ, " . ", "."), " [ ]", "[]")
		typ = functionTypeKey(s, typ)
		defaultValue = sqlDefinitionKey(defaultKey(s, defaultValue, typ))
		if mode != "OUT" {
			inputs = append(inputs, typ)
		}
		decls = append(decls, mode+"\x00"+sqlDefinitionKey(name)+"\x00"+typ+"\x00"+defaultValue)
	}
	return strings.Join(inputs, ", "), strings.Join(decls, "\x01"), nil
}

func functionChanges(current, desired schemigo.Schema) ([]functionChange, error) {
	old, newFuncs := map[string]schemigo.Function{}, map[string]schemigo.Function{}
	declarations := map[string]string{}
	for side, s := range []schemigo.Schema{current, desired} {
		dest := old
		if side == 1 {
			dest = newFuncs
		}
		for _, f := range s.Functions {
			if err := postgresName(f.Name); err != nil {
				return nil, err
			}
			if f.Language != "sql" && f.Language != "plpgsql" {
				return nil, fmt.Errorf("function %q: unsupported language %q", f.Name, f.Language)
			}
			if strings.TrimSpace(f.Returns) == "" || strings.TrimSpace(f.Body) == "" || strings.ContainsRune(f.Body, 0) {
				return nil, fmt.Errorf("function %q: returns and body must be nonempty SQL without NUL", f.Name)
			}
			args, decl, err := functionArguments(s, f.Parameters)
			if err != nil {
				return nil, fmt.Errorf("function %q: %w", f.Name, err)
			}
			key := f.Name + "\x00" + args
			if _, exists := dest[key]; exists {
				return nil, fmt.Errorf("duplicate PostgreSQL function signature %q(%s)", f.Name, args)
			}
			dest[key] = f
			declarations[fmt.Sprint(side)+key] = decl
		}
	}
	keys := []string{}
	for k := range old {
		keys = append(keys, k)
	}
	for k := range newFuncs {
		if _, ok := old[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var result []functionChange
	for _, key := range keys {
		before, hasOld := old[key]
		after, hasNew := newFuncs[key]
		if hasOld && hasNew && before.Language == after.Language && before.Body == after.Body &&
			functionReturnKey(current, before.Returns) == functionReturnKey(desired, after.Returns) && declarations["0"+key] == declarations["1"+key] {
			continue
		}
		c := functionChange{identity: strings.SplitN(key, "\x00", 2)[1]}
		if hasOld {
			c.old = &before
		}
		if hasNew {
			c.next = &after
		}
		result = append(result, c)
	}
	return orderFunctionChanges(desired.Name, result)
}

func functionTypeKey(s schemigo.Schema, raw string) string {
	if key, ok := nativeTypeKey(raw); ok {
		return typeModifier.ReplaceAllString(key, "")
	}
	return columnTypeKey(s, raw)
}

func functionReturnKey(s schemigo.Schema, raw string) string {
	tokens := expressionTokens(raw)
	if len(tokens) > 3 && strings.EqualFold(tokens[0], "TABLE") && tokens[1] == "(" && tokens[len(tokens)-1] == ")" {
		_, declaration, err := functionArguments(s, strings.Join(tokens[2:len(tokens)-1], " "))
		if err == nil {
			return "TABLE:" + declaration
		}
	}
	if len(tokens) > 1 && strings.EqualFold(tokens[0], "SETOF") {
		return "SETOF:" + functionTypeKey(s, strings.Join(tokens[1:], " "))
	}
	return functionTypeKey(s, raw)
}

func orderFunctionChanges(schema string, changes []functionChange) ([]functionChange, error) {
	state := make([]int, len(changes))
	var ordered []functionChange
	var visit func(int) error
	visit = func(i int) error {
		if state[i] == 2 {
			return nil
		}
		if state[i] == 1 {
			return fmt.Errorf("cyclic function creation dependency")
		}
		state[i] = 1
		if f := changes[i].next; f != nil {
			tokens := expressionTokens(f.Body)
			for j, c := range changes {
				if i == j || c.next == nil {
					continue
				}
				for k := 0; k+1 < len(tokens); k++ {
					if tokens[k+1] != "(" || sqlIdentifier(tokens[k]) != c.next.Name {
						continue
					}
					if k > 0 && tokens[k-1] == "." && (k < 2 || sqlIdentifier(tokens[k-2]) != schema) {
						continue
					}
					if err := visit(j); err != nil {
						return err
					}
					break
				}
			}
		}
		state[i] = 2
		ordered = append(ordered, changes[i])
		return nil
	}
	for i := range changes {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func sqlIdentifier(token string) string {
	if strings.HasPrefix(token, "\"") {
		return strings.ReplaceAll(strings.Trim(token, "\""), "\"\"", "\"")
	}
	return strings.ToLower(token)
}

func functionNeedsDrop(c functionChange) bool {
	if c.old == nil {
		return false
	}
	if c.next == nil {
		return true
	}
	_, before, _ := functionArguments(schemigo.Schema{}, c.old.Parameters)
	_, after, _ := functionArguments(schemigo.Schema{}, c.next.Parameters)
	defaults := func(declaration string) int {
		count := 0
		for _, argument := range strings.Split(declaration, "\x01") {
			fields := strings.SplitN(argument, "\x00", 4)
			if len(fields) == 4 && fields[3] != "" {
				count++
			}
		}
		return count
	}
	if defaults(after) < defaults(before) {
		return true
	}
	stripDefaults := func(s string) string {
		parts := strings.Split(s, "\x01")
		for i, p := range parts {
			fields := strings.Split(p, "\x00")
			if len(fields) >= 4 {
				parts[i] = strings.Join(fields[:3], "\x00")
			}
		}
		return strings.Join(parts, "\x01")
	}
	return functionReturnKey(schemigo.Schema{}, c.old.Returns) != functionReturnKey(schemigo.Schema{}, c.next.Returns) || stripDefaults(before) != stripDefaults(after)
}

func createFunction(schema string, f schemigo.Function) schemigo.Statement {
	return tableSchemaScope(schema, tableDDL("CREATE OR REPLACE FUNCTION "+Qualified(schema, f.Name)+"("+f.Parameters+") RETURNS "+f.Returns+" LANGUAGE "+QuoteIdent(f.Language)+" AS "+dollarQuote(f.Body), false))
}
func dropFunction(schema string, c functionChange) schemigo.Statement {
	return tableSchemaScope(schema, tableDDL("DROP FUNCTION IF EXISTS "+Qualified(schema, c.old.Name)+"("+c.identity+")", true))
}

func normalizeTriggers(s *schemigo.Schema) error {
	for i := range s.Triggers {
		t := &s.Triggers[i]
		if err := postgresName(t.Name); err != nil {
			return err
		}
		t.When = strings.ToUpper(strings.TrimSpace(t.When))
		events := strings.Split(strings.ToUpper(t.Event), " OR ")
		seen := map[string]bool{}
		for _, e := range events {
			e = strings.TrimSpace(e)
			if (e != "INSERT" && e != "DELETE" && e != "UPDATE" && e != "TRUNCATE") || seen[e] {
				return fmt.Errorf("trigger %q: invalid or duplicate event %q", t.Name, e)
			}
			seen[e] = true
		}
		events = nil
		for _, e := range []string{"INSERT", "DELETE", "UPDATE", "TRUNCATE"} {
			if seen[e] {
				events = append(events, e)
			}
		}
		t.Event = strings.Join(events, " OR ")
		if t.When != "BEFORE" && t.When != "AFTER" && t.When != "INSTEAD OF" {
			return fmt.Errorf("trigger %q: invalid timing", t.Name)
		}
		isView := slices.ContainsFunc(s.Views, func(v schemigo.View) bool { return v.Name == t.Table })
		if seen["TRUNCATE"] && (t.ForEach == "ROW" || isView) || t.When == "INSTEAD OF" && (!isView || t.ForEach != "ROW" || t.Condition != "") || isView && t.When != "INSTEAD OF" && t.ForEach == "ROW" {
			return fmt.Errorf("trigger %q: incompatible relation, timing or row mode", t.Name)
		}
		validFunction := slices.ContainsFunc(s.Functions, func(f schemigo.Function) bool {
			return f.Name == t.Function && strings.TrimSpace(f.Parameters) == "" && strings.EqualFold(strings.TrimSpace(f.Returns), "trigger")
		})
		if !validFunction {
			return fmt.Errorf("trigger %q requires a zero-argument function returning trigger", t.Name)
		}
	}
	return nil
}

func triggerKey(t schemigo.Trigger) string { return t.Table + "\x00" + t.Name }
func sameTrigger(a, b schemigo.Trigger) bool {
	a.Condition = sqlDefinitionKey(a.Condition)
	b.Condition = sqlDefinitionKey(b.Condition)
	return a == b
}
func createTrigger(schema string, t schemigo.Trigger) schemigo.Statement {
	command := "CREATE OR REPLACE TRIGGER " + QuoteIdent(t.Name) + " " + t.When + " " + t.Event + " ON " + Qualified(schema, t.Table) + " FOR EACH " + t.ForEach
	if t.Condition != "" {
		command += " WHEN (" + t.Condition + ")"
	}
	command += " EXECUTE FUNCTION " + Qualified(schema, t.Function) + "()"
	return tableSchemaScope(schema, tableDDL(command, false))
}
func dropTrigger(schema string, t schemigo.Trigger) schemigo.Statement {
	sql := "IF pg_catalog.to_regclass(" + QuoteLiteral(Qualified(schema, t.Table)) + ") IS NOT NULL THEN DROP TRIGGER IF EXISTS " + QuoteIdent(t.Name) + " ON " + Qualified(schema, t.Table) + "; END IF;"
	return tableDDL("DO "+QuoteLiteral("BEGIN "+sql+" END"), false)
}
