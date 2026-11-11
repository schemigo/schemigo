package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/schemigo/schemigo"
)

// Diff plans table, column, key, index, CHECK, extension and enum changes without executing DDL.
// Function, trigger and view changes return errors.ErrUnsupported until their phases are implemented.
// Both schemas must name the same namespace. Enum rebuilds require conn for
// dependency discovery; creates and additive changes can be planned without it.
func (*Driver) Diff(ctx context.Context, conn *sql.Conn, current, desired schemigo.Schema) (schemigo.Plan, error) {
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
	canonicalTableProperties(&current)
	canonicalTableProperties(&desired)
	if current.Name == "" || current.Name != desired.Name {
		return schemigo.Plan{}, fmt.Errorf("diff requires the same nonempty schema name: %q, %q", current.Name, desired.Name)
	}
	for _, s := range []schemigo.Schema{current, desired} {
		if err := postgresName(s.Name); err != nil {
			return schemigo.Plan{}, err
		}
		for _, e := range s.Enums {
			if err := postgresName(e.Name); err != nil {
				return schemigo.Plan{}, err
			}
			for _, v := range e.Values {
				if len(v) > 63 || strings.ContainsRune(v, 0) {
					return schemigo.Plan{}, fmt.Errorf("enum %q: invalid label %q", e.Name, v)
				}
			}
		}
		for _, e := range s.Extensions {
			if err := postgresName(e.Name); err != nil {
				return schemigo.Plan{}, err
			}
			if strings.ContainsRune(e.Version, 0) {
				return schemigo.Plan{}, fmt.Errorf("extension %q: invalid version", e.Name)
			}
		}
	}
	// Slice ordering of independently named objects is not a schema change.
	for _, oldEnum := range current.Enums {
		present := slices.ContainsFunc(desired.Enums, func(e schemigo.Enum) bool { return e.Name == oldEnum.Name })
		if present {
			continue
		}
		for _, table := range desired.Tables {
			for _, column := range table.Columns {
				if strings.TrimSuffix(columnTypeKey(current, column.Type), "[]") == Qualified(current.Name, oldEnum.Name) {
					return schemigo.Plan{}, fmt.Errorf("desired column %s.%s still references removed enum %q", Qualified(desired.Name, table.Name), QuoteIdent(column.Name), oldEnum.Name)
				}
			}
		}
	}
	for _, s := range []*schemigo.Schema{&current, &desired} {
		slices.SortFunc(s.Tables, func(a, b schemigo.Table) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(s.Views, func(a, b schemigo.View) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(s.Functions, func(a, b schemigo.Function) int {
			return strings.Compare(a.Name+"\x00"+a.Parameters, b.Name+"\x00"+b.Parameters)
		})
		slices.SortFunc(s.Triggers, func(a, b schemigo.Trigger) int { return strings.Compare(a.Table+"\x00"+a.Name, b.Table+"\x00"+b.Name) })
	}
	enums, columns, err := diffEnums(ctx, conn, current, desired)
	if err != nil {
		return schemigo.Plan{}, err
	}
	for _, column := range columns {
		for i := range current.Tables {
			if current.Tables[i].Name == column.Table {
				for j := range current.Tables[i].Columns {
					if current.Tables[i].Columns[j].Name == column.Name {
						current.Tables[i].Columns[j].Default = column.Default
					}
				}
			}
		}
	}
	tables, err := diffTables(&current, desired)
	if err != nil {
		return schemigo.Plan{}, err
	}
	for i := range tables {
		tables[i] = tableSchemaScope(desired.Name, tables[i])
	}
	slices.SortFunc(current.Triggers, func(a, b schemigo.Trigger) int { return strings.Compare(a.Table+"\x00"+a.Name, b.Table+"\x00"+b.Name) })
	if !reflect.DeepEqual(current.Views, desired.Views) || !reflect.DeepEqual(current.Functions, desired.Functions) || !reflect.DeepEqual(current.Triggers, desired.Triggers) {
		return schemigo.Plan{}, fmt.Errorf("functions, triggers or views changed: %w", errors.ErrUnsupported)
	}
	// Drops follow table operations; creates/additions precede consumers of enum types.
	var enumDrops []schemigo.Statement
	activeEnums := []schemigo.Statement{}
	for _, statement := range enums {
		if strings.HasPrefix(statement.SQL, "DROP TYPE IF EXISTS ") {
			enumDrops = append(enumDrops, statement)
		} else {
			activeEnums = append(activeEnums, statement)
		}
	}
	var plan schemigo.Plan
	enumsEmitted := false
	for _, kind := range desired.Order {
		switch kind {
		case schemigo.EntityExtensions:
			plan.Statements = append(plan.Statements, diffExtensions(current, desired)...)
		case schemigo.EntityEnums:
			if !enumsEmitted {
				plan.Statements = append(plan.Statements, activeEnums...)
				enumsEmitted = true
			}
		case schemigo.EntityTables:
			if !enumsEmitted && len(tables) > 0 && tableUsesChangedEnum(current, desired) {
				plan.Statements = append(plan.Statements, activeEnums...)
				enumsEmitted = true
			}
			plan.Statements = append(plan.Statements, tables...)
		}
	}
	plan.Statements = append(plan.Statements, enumDrops...)
	return plan, nil
}

func tableUsesChangedEnum(current, desired schemigo.Schema) bool {
	old := map[string][]string{}
	for _, e := range current.Enums {
		old[e.Name] = e.Values
	}
	for _, e := range desired.Enums {
		if slices.Equal(old[e.Name], e.Values) {
			continue
		}
		for _, table := range desired.Tables {
			for _, column := range table.Columns {
				if strings.TrimSuffix(columnTypeKey(desired, column.Type), "[]") == Qualified(desired.Name, e.Name) {
					return true
				}
			}
		}
	}
	return false
}

func postgresName(name string) error {
	if len(name) == 0 || len(name) > 63 || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid PostgreSQL identifier %q", name)
	}
	return nil
}

func diffExtensions(current, desired schemigo.Schema) []schemigo.Statement {
	existing := map[string]string{}
	for _, e := range current.Extensions {
		existing[e.Name] = e.Version
	}
	extensions := slices.Clone(desired.Extensions)
	slices.SortFunc(extensions, func(a, b schemigo.Extension) int { return strings.Compare(a.Name, b.Name) })
	var result []schemigo.Statement
	for _, e := range extensions {
		version, exists := existing[e.Name]
		if exists && (e.Version == "" || e.Version == version) {
			continue
		}
		command := "CREATE EXTENSION IF NOT EXISTS " + QuoteIdent(e.Name) + " WITH SCHEMA " + QuoteIdent(desired.Name)
		if e.Version != "" {
			command += " VERSION " + QuoteLiteral(e.Version)
		}
		body := "BEGIN\n" + command + ";\nIF EXISTS (SELECT FROM pg_catalog.pg_extension e JOIN pg_catalog.pg_namespace n ON n.oid=e.extnamespace WHERE e.extname=" + QuoteLiteral(e.Name) + " AND n.nspname<>" + QuoteLiteral(desired.Name) + ") THEN RAISE EXCEPTION 'extension belongs to another schema'; END IF;\n"
		if e.Version != "" {
			body += "IF EXISTS (SELECT FROM pg_catalog.pg_extension WHERE extname=" + QuoteLiteral(e.Name) + " AND extversion<>" + QuoteLiteral(e.Version) + ") THEN ALTER EXTENSION " + QuoteIdent(e.Name) + " UPDATE TO " + QuoteLiteral(e.Version) + "; END IF;\n"
		}
		body += "END"
		result = append(result, schemigo.Statement{SQL: "DO " + QuoteLiteral(body), SkipExplain: true})
	}
	return result
}
