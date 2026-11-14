package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

// Inspect reads tables, columns, skipping indexes, constraints and views in database.
// SQL functions belong to the whole server and are included in every inspection.
// Materialized view Definition includes its TO/ENGINE clause followed by AS SELECT.
func (*Driver) Inspect(ctx context.Context, conn *sql.Conn, database string) (schemigo.Schema, error) {
	s := schemigo.Schema{Name: database}
	if err := validName(database); err != nil {
		return s, err
	}
	var exists uint64
	if err := conn.QueryRowContext(ctx, "SELECT count() FROM system.databases WHERE name = ?", database).Scan(&exists); err != nil {
		return s, err
	}
	if exists == 0 {
		return s, fmt.Errorf("ClickHouse database %q does not exist", database)
	}
	rows, err := conn.QueryContext(ctx, `SELECT name, engine, engine_full, sorting_key, partition_key, primary_key, create_table_query FROM system.tables WHERE database = ? AND name != 'schema_migrations' AND NOT startsWith(name, '.inner') ORDER BY name`, database)
	if err != nil {
		return s, fmt.Errorf("inspect tables: %w", err)
	}
	for rows.Next() {
		var name, engine, full, order, partition, primary, create string
		if err = rows.Scan(&name, &engine, &full, &order, &partition, &primary, &create); err != nil {
			break
		}
		if engine == "View" || engine == "MaterializedView" {
			var definition string
			definition, err = viewDefinition(create, engine == "MaterializedView")
			if err != nil {
				break
			}
			s.Views = append(s.Views, schemigo.View{Name: name, Definition: definition, Materialized: engine == "MaterializedView"})
			continue
		}
		engineSQL, parseErr := tableEngine(full)
		if parseErr != nil {
			err = fmt.Errorf("table %q: %w", name, parseErr)
			break
		}
		t := schemigo.Table{Name: name, Engine: engineSQL, OrderBy: keyExpression(order), PartitionBy: keyExpression(partition)}
		if order == "" && strings.HasSuffix(engine, "MergeTree") {
			t.OrderBy = "tuple()"
		}
		// ClickHouse implicitly uses ORDER BY as its primary key. Keep only an explicit difference.
		if canonical(primary) != canonical(order) {
			for _, tok := range sqlTokens(primary) {
				if tok.text == "," || tok.text == "(" || tok.text == ")" {
					continue
				}
				t.PrimaryKey = append(t.PrimaryKey, strings.Trim(tok.text, "`\""))
			}
		}
		t.Checks, err = tableChecks(create)
		if err != nil {
			break
		}
		s.Tables = append(s.Tables, t)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return s, err
	}
	if readErr != nil {
		return s, readErr
	}
	if closeErr != nil {
		return s, closeErr
	}
	for i := range s.Tables {
		if err := inspectTable(ctx, conn, database, &s.Tables[i]); err != nil {
			return s, err
		}
	}
	rows, err = conn.QueryContext(ctx, "SELECT name, create_query FROM system.functions WHERE origin = 'SQLUserDefined' ORDER BY name")
	if err != nil {
		return s, fmt.Errorf("inspect SQL functions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, create string
		if err := rows.Scan(&name, &create); err != nil {
			return s, err
		}
		f, err := functionDefinition(name, create)
		if err != nil {
			return s, err
		}
		s.Functions = append(s.Functions, f)
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	return normalize(s)
}
func inspectTable(ctx context.Context, conn *sql.Conn, database string, t *schemigo.Table) error {
	rows, err := conn.QueryContext(ctx, `SELECT name, type, default_kind, default_expression FROM system.columns WHERE database = ? AND table = ? ORDER BY position`, database, t.Name)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c schemigo.Column
		var kind string
		if err = rows.Scan(&c.Name, &c.Type, &kind, &c.Default); err != nil {
			break
		}
		if kind != "" && kind != "DEFAULT" {
			err = fmt.Errorf("table %q column %q: %s columns cannot be represented", t.Name, c.Name, kind)
			break
		}
		t.Columns = append(t.Columns, c)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	rows, err = conn.QueryContext(ctx, `SELECT name, expr, type_full, granularity FROM system.data_skipping_indices WHERE database = ? AND table = ? ORDER BY name`, database, t.Name)
	if err != nil {
		return err
	}
	for rows.Next() {
		var idx schemigo.Index
		if err = rows.Scan(&idx.Name, &idx.Expression, &idx.Type, &idx.Granularity); err != nil {
			break
		}
		t.Indexes = append(t.Indexes, idx)
	}
	readErr = rows.Err()
	closeErr = rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func viewDefinition(create string, materialized bool) (string, error) {
	ts := sqlTokens(create)
	// Skip CREATE [MATERIALIZED] VIEW and its qualified name.
	i := 0
	for i < len(ts) && !strings.EqualFold(ts[i].text, "VIEW") {
		i++
	}
	i += 2
	if i < len(ts) && ts[i].text == "." {
		i += 2
	}
	if i >= len(ts) {
		return "", fmt.Errorf("cannot read view definition: %s", create)
	}
	if materialized {
		return strings.TrimSpace(create[ts[i].start:]), nil
	}
	suffix := storageDefinition(strings.TrimSpace(create[ts[i].start:]))
	if _, security := withoutSecurity(suffix); security != "" {
		return suffix, nil
	}
	depth := 0
	for ; i < len(ts); i++ {
		switch ts[i].text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && strings.EqualFold(ts[i].text, "AS") {
			return strings.TrimSpace(create[ts[i].end:]), nil
		}
	}
	return "", fmt.Errorf("cannot read view SELECT: %s", create)
}
func functionDefinition(name, create string) (schemigo.Function, error) {
	f := schemigo.Function{Name: name}
	ts := sqlTokens(create)
	for i, t := range ts {
		if strings.EqualFold(t.text, "AS") && i+1 < len(ts) {
			lambda := strings.TrimSpace(create[ts[i+1].start:])
			arrow := strings.Index(lambda, "->")
			if arrow < 0 {
				break
			}
			f.Parameters = strings.TrimSpace(lambda[:arrow])
			f.Parameters = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(f.Parameters, "("), ")"))
			f.Body = strings.TrimSpace(lambda[arrow+2:])
			return f, nil
		}
	}
	return f, fmt.Errorf("cannot read SQL function %q", name)
}

func tableEngine(full string) (string, error) {
	tokens := sqlTokens(full)
	depth := 0
	end := len(full)
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
		switch strings.ToUpper(t.text) {
		case "ORDER", "PARTITION", "PRIMARY":
			if end == len(full) {
				end = t.start
			}
		case "SAMPLE", "TTL":
			return "", fmt.Errorf("%s table clauses cannot be represented by the schema model", t.text)
		case "SETTINGS":
			base := full[:end]
			if end == len(full) {
				base = full[:t.start]
			}
			return strings.TrimSpace(base) + " " + strings.TrimSpace(full[t.start:]), nil
		}
	}
	return strings.TrimSpace(full[:end]), nil
}
func keyExpression(key string) string {
	depth := 0
	for _, t := range sqlTokens(key) {
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				return "(" + key + ")"
			}
		}
	}
	return key
}

func tableChecks(create string) ([]schemigo.Check, error) {
	var checks []schemigo.Check
	tokens := sqlTokens(create)
	depth := 0
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 1 || !strings.EqualFold(t.text, "CONSTRAINT") {
			continue
		}
		if i+3 >= len(tokens) || !strings.EqualFold(tokens[i+2].text, "CHECK") {
			return nil, fmt.Errorf("only CHECK constraints can be represented")
		}
		name := strings.Trim(tokens[i+1].text, "`\"")
		name = strings.NewReplacer("\\`", "`", "\\\"", "\"", "\\\\", "\\").Replace(name)
		start := tokens[i+3].start
		i += 3
		for ; i < len(tokens); i++ {
			if depth == 1 && (tokens[i].text == "," || tokens[i].text == ")") {
				break
			}
			switch tokens[i].text {
			case "(":
				depth++
			case ")":
				depth--
			}
		}
		if i >= len(tokens) {
			return nil, fmt.Errorf("unterminated CHECK constraint %q", name)
		}
		checks = append(checks, schemigo.Check{Name: name, Expression: strings.TrimSpace(create[start:tokens[i].start])})
		i--
	}
	return checks, nil
}
