package postgres_test

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/postgres"
)

func tableSchema(tables ...schemigo.Table) schemigo.Schema {
	return schemigo.Schema{Name: "public", Tables: tables}
}
func simpleTable(name string) schemigo.Table {
	return schemigo.Table{Name: name, Columns: []schemigo.Column{{Name: "id", Type: "bigint"}}}
}
func tablePlan(t *testing.T, current, desired schemigo.Schema) schemigo.Plan {
	t.Helper()
	p, err := postgres.New().Diff(t.Context(), nil, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiffRenameTable(t *testing.T) {
	p := tablePlan(t, tableSchema(simpleTable("old")), tableSchema(simpleTable("new")))
	if len(p.Statements) != 1 || !strings.Contains(p.SQL(), `RENAME TO "new"`) || strings.Contains(p.SQL(), "DROP TABLE") || p.Statements[0].Destructive {
		t.Fatalf("plan=%+v", p)
	}
}
func TestDiffRenameColumn(t *testing.T) {
	old := simpleTable("users")
	desired := simpleTable("users")
	desired.Columns[0].Name = "user_id"
	old.PrimaryKey = []string{"id"}
	desired.PrimaryKey = []string{"user_id"}
	p := tablePlan(t, tableSchema(old), tableSchema(desired))
	if len(p.Statements) != 1 || !strings.Contains(p.SQL(), `RENAME COLUMN "id" TO "user_id"`) || strings.Contains(p.SQL(), "DROP COLUMN") || p.Statements[0].Destructive {
		t.Fatalf("plan=%+v", p)
	}
}
func TestDiffRenameAmbiguous(t *testing.T) {
	for _, tt := range []struct {
		name     string
		old, new []schemigo.Table
	}{
		{name: "two old", old: []schemigo.Table{simpleTable("a"), simpleTable("b")}, new: []schemigo.Table{simpleTable("c")}},
		{name: "two new", old: []schemigo.Table{simpleTable("a")}, new: []schemigo.Table{simpleTable("b"), simpleTable("c")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := postgres.New().Diff(t.Context(), nil, tableSchema(tt.old...), tableSchema(tt.new...))
			if err == nil || !strings.Contains(err.Error(), "ambiguous") || len(p.Statements) > 0 {
				t.Fatalf("plan=%+v, err=%v", p, err)
			}
		})
	}
	old := schemigo.Table{Name: "t", Columns: []schemigo.Column{{Name: "a", Type: "text"}, {Name: "b", Type: "text"}}}
	desired := schemigo.Table{Name: "t", Columns: []schemigo.Column{{Name: "c", Type: "text"}}}
	if _, err := postgres.New().Diff(t.Context(), nil, tableSchema(old), tableSchema(desired)); err == nil {
		t.Fatal("ambiguous columns accepted")
	}
}
func TestDiffColumnUsing(t *testing.T) {
	old := simpleTable("t")
	old.Columns[0].Type = "text"
	old.Columns[0].Default = "'1'"
	desired := simpleTable("t")
	desired.Columns[0].Using = `"id"::bigint + 1`
	desired.Columns[0].Default = "2"
	desired.Columns[0].NotNull = true
	p := tablePlan(t, tableSchema(old), tableSchema(desired))
	if !strings.Contains(p.SQL(), `USING "id"::bigint + 1`) || !strings.Contains(p.SQL(), "DROP DEFAULT") || !strings.Contains(p.SQL(), "SET NOT NULL") {
		t.Fatalf("plan=%s", p.SQL())
	}
	if !p.Statements[0].Destructive {
		t.Fatal("type conversion not marked destructive")
	}
}
func TestDiffQuotedOrderTable(t *testing.T) {
	desired := simpleTable(`User"Archive`)
	desired.Columns[0].Name = "order"
	desired.Columns[0].PrimaryKey = true
	p := tablePlan(t, tableSchema(), tableSchema(desired))
	if !strings.Contains(p.SQL(), `CREATE TABLE IF NOT EXISTS "public"."User""Archive"`) || !strings.Contains(p.SQL(), `"order" bigint`) || !strings.Contains(p.SQL(), "PRIMARY KEY") {
		t.Fatal(p.SQL())
	}
}
func TestDiffColumnAndConstraintScope(t *testing.T) {
	desired := simpleTable("t")
	desired.Columns = append(desired.Columns, schemigo.Column{Name: "x", Type: "text"})
	p := tablePlan(t, tableSchema(simpleTable("t")), tableSchema(desired))
	if !strings.Contains(p.SQL(), `ADD COLUMN IF NOT EXISTS "x" text`) {
		t.Fatal(p.SQL())
	}
	desired.Checks = []schemigo.Check{{Name: "x", Expression: "id > 0"}}
	p = tablePlan(t, tableSchema(simpleTable("t")), tableSchema(desired))
	if !strings.Contains(p.SQL(), `ADD CONSTRAINT "x" CHECK (id > 0)`) {
		t.Fatal(p.SQL())
	}
}
func TestDiffColumnAliasesAndHints(t *testing.T) {
	old := simpleTable("t")
	old.Columns[0].Default = "0::bigint"
	desired := simpleTable("t")
	desired.Columns[0].Type = "int8"
	desired.Columns[0].Default = "0"
	desired.Columns[0].Using = "id + 1"
	if p := tablePlan(t, tableSchema(old), tableSchema(desired)); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
}

func TestDiffTableDropOrder(t *testing.T) {
	parent := simpleTable("parent")
	parent.PrimaryKey = []string{"id"}
	child := simpleTable("z_child")
	child.ForeignKeys = []schemigo.ForeignKey{{Name: "fk", Columns: []string{"id"}, ReferencedTable: "parent", ReferencedColumns: []string{"id"}}}
	p := tablePlan(t, tableSchema(parent, child), tableSchema())
	if len(p.Statements) != 3 || !strings.Contains(p.Statements[0].SQL, `DROP CONSTRAINT IF EXISTS "fk"`) || !strings.Contains(p.Statements[1].SQL, `DROP TABLE IF EXISTS`) {
		t.Fatal(p.SQL())
	}
	parent.ForeignKeys = []schemigo.ForeignKey{{Name: "back", Columns: []string{"id"}, ReferencedTable: "z_child", ReferencedColumns: []string{"id"}}}
	child.PrimaryKey = []string{"id"}
	p = tablePlan(t, tableSchema(parent, child), tableSchema())
	if len(p.Statements) != 4 || !strings.Contains(p.Statements[0].SQL, "DROP CONSTRAINT") || !strings.Contains(p.Statements[1].SQL, "DROP CONSTRAINT") {
		t.Fatalf("cyclic FK drop plan=%+v", p)
	}
}

func TestDiffRemovedEnumStillReferenced(t *testing.T) {
	current := enumSchema("a")
	current.Tables = []schemigo.Table{{Name: "old", Columns: []schemigo.Column{{Name: "value", Type: "status"}}}}
	desired := tableSchema(schemigo.Table{Name: "new", Columns: []schemigo.Column{{Name: "value", Type: "status"}}})
	p, err := postgres.New().Diff(t.Context(), nil, current, desired)
	if err == nil || len(p.Statements) > 0 {
		t.Fatalf("invalid enum removal produced table replacement: %+v, %v", p, err)
	}
}
