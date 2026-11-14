package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/schemigo/schemigo"
)

// Diff plans supported MySQL schema changes without executing SQL.
func (*Driver) Diff(ctx context.Context, _ *sql.Conn, current, desired schemigo.Schema) (schemigo.Plan, error) {
	if err := ctx.Err(); err != nil {
		return schemigo.Plan{}, err
	}
	var err error
	current, err = schemigo.Normalize(current)
	if err != nil {
		return schemigo.Plan{}, fmt.Errorf("current schema: %w", err)
	}
	desired, err = schemigo.Normalize(desired)
	if err != nil {
		return schemigo.Plan{}, fmt.Errorf("desired schema: %w", err)
	}
	if current.Name == "" || current.Name != desired.Name {
		return schemigo.Plan{}, fmt.Errorf("MySQL diff requires the same nonempty database name")
	}
	if err := validateMySQLSchema(current); err != nil {
		return schemigo.Plan{}, fmt.Errorf("current schema: %w", err)
	}
	if err := validateMySQLSchema(desired); err != nil {
		return schemigo.Plan{}, fmt.Errorf("desired schema: %w", err)
	}
	var plan schemigo.Plan
	changedRoutines := map[string]bool{}
	for _, old := range current.Functions {
		if next, ok := findFunction(desired.Functions, old.Name); !ok || !sameFunction(old, next) {
			changedRoutines[old.Name] = true
		}
	}
	for _, old := range current.Triggers {
		next, found := findTrigger(desired.Triggers, old.Name)
		if !found || old != next || changedRoutines[old.Function] || tableChanged(current.Tables, desired.Tables, old.Table) {
			plan.Statements = append(plan.Statements, ddl("DROP TRIGGER IF EXISTS "+Qualified(current.Name, old.Name), true))
		}
	}
	for _, old := range current.Views {
		next, found := findView(desired.Views, old.Name)
		if !found || strings.TrimSpace(old.Definition) != strings.TrimSpace(next.Definition) {
			plan.Statements = append(plan.Statements, ddl("DROP VIEW IF EXISTS "+Qualified(current.Name, old.Name), true))
		}
	}
	tableStatements, err := diffTables(current, desired)
	if err != nil {
		return schemigo.Plan{}, err
	}
	plan.Statements = append(plan.Statements, tableStatements...)
	routineStatements, err := diffRoutines(current, desired)
	if err != nil {
		return schemigo.Plan{}, err
	}
	plan.Statements = append(plan.Statements, routineStatements...)
	for _, next := range desired.Views {
		old, found := findView(current.Views, next.Name)
		if !found || strings.TrimSpace(old.Definition) != strings.TrimSpace(next.Definition) {
			plan.Statements = append(plan.Statements, ddl("CREATE OR REPLACE VIEW "+Qualified(desired.Name, next.Name)+" AS "+strings.TrimSpace(next.Definition), false))
		}
	}
	for _, next := range desired.Triggers {
		old, found := findTrigger(current.Triggers, next.Name)
		if !found || old != next || changedRoutines[next.Function] || tableChanged(current.Tables, desired.Tables, next.Table) {
			plan.Statements = append(plan.Statements, ddl("CREATE TRIGGER "+Qualified(desired.Name, next.Name)+" "+next.When+" "+next.Event+" ON "+Qualified(desired.Name, next.Table)+" FOR EACH ROW CALL "+Qualified(desired.Name, next.Function)+"()", false))
		}
	}
	return plan, nil
}

func ddl(sql string, destructive bool) schemigo.Statement {
	return schemigo.Statement{SQL: sql, Destructive: destructive, SkipExplain: true, OutsideTransaction: true}
}

func validateMySQLSchema(schema schemigo.Schema) error {
	if len(schema.Extensions) > 0 || len(schema.Enums) > 0 {
		return fmt.Errorf("MySQL does not support extensions or standalone enum objects")
	}
	for _, table := range schema.Tables {
		if table.Engine != "" || table.OrderBy != "" || table.PartitionBy != "" {
			return fmt.Errorf("table %q: ClickHouse options are unsupported", table.Name)
		}
		for _, column := range table.Columns {
			if column.Using != "" {
				return fmt.Errorf("table %q column %q: USING is unsupported", table.Name, column.Name)
			}
		}
		for _, index := range table.Indexes {
			if index.Where != "" || index.Type != "" || index.Granularity != 0 || index.Method != "" && !strings.EqualFold(index.Method, "btree") {
				return fmt.Errorf("table %q index %q: unsupported index options", table.Name, index.Name)
			}
		}
	}
	for _, view := range schema.Views {
		if view.Materialized {
			return fmt.Errorf("view %q: materialized views are unsupported", view.Name)
		}
		if strings.TrimSpace(view.Definition) == "" {
			return fmt.Errorf("view %q: definition is empty", view.Name)
		}
	}
	routineNames := map[string]bool{}
	for _, function := range schema.Functions {
		if routineNames[function.Name] {
			return fmt.Errorf("routine %q: MySQL does not support overloaded names", function.Name)
		}
		routineNames[function.Name] = true
		if function.Language != "" && !strings.EqualFold(function.Language, "sql") {
			return fmt.Errorf("routine %q: only SQL language is supported", function.Name)
		}
		if strings.TrimSpace(function.Body) == "" {
			return fmt.Errorf("routine %q: body is empty", function.Name)
		}
	}
	for _, trigger := range schema.Triggers {
		if trigger.ForEach != "ROW" || trigger.Condition != "" || trigger.When != "BEFORE" && trigger.When != "AFTER" || trigger.Event != "INSERT" && trigger.Event != "UPDATE" && trigger.Event != "DELETE" {
			return fmt.Errorf("trigger %q: unsupported MySQL trigger options", trigger.Name)
		}
		function, found := findFunction(schema.Functions, trigger.Function)
		if !found || function.Returns != "" || function.Parameters != "" {
			return fmt.Errorf("trigger %q: target must be a zero-argument procedure", trigger.Name)
		}
	}
	return nil
}

func findView(views []schemigo.View, name string) (schemigo.View, bool) {
	for _, view := range views {
		if view.Name == name {
			return view, true
		}
	}
	return schemigo.View{}, false
}

func findTrigger(triggers []schemigo.Trigger, name string) (schemigo.Trigger, bool) {
	for _, trigger := range triggers {
		if trigger.Name == name {
			return trigger, true
		}
	}
	return schemigo.Trigger{}, false
}

func findFunction(functions []schemigo.Function, name string) (schemigo.Function, bool) {
	for _, function := range functions {
		if function.Name == name {
			return function, true
		}
	}
	return schemigo.Function{}, false
}

func sameFunction(a, b schemigo.Function) bool {
	return a.Name == b.Name && a.Parameters == b.Parameters && a.Returns == b.Returns && a.Body == b.Body && strings.EqualFold(a.Language, b.Language)
}

func tableChanged(current, desired []schemigo.Table, name string) bool {
	old := slices.IndexFunc(current, func(table schemigo.Table) bool { return table.Name == name })
	next := slices.IndexFunc(desired, func(table schemigo.Table) bool { return table.Name == name })
	if old < 0 || next < 0 {
		return true
	}
	return !sameTable(current[old], desired[next])
}
