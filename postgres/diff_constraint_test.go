package postgres_test

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/postgres"
)

func TestDiffIndex(t *testing.T) {
	table := schemigo.Table{Name: "orders", Columns: []schemigo.Column{
		{Name: "email", Type: "text"}, {Name: "active", Type: "boolean"}, {Name: "data", Type: "jsonb"},
	}, Indexes: []schemigo.Index{
		{Name: "idx_orders_active", Columns: []string{"email"}, Where: "active"},
		{Name: "idx_orders_lower_email", Expression: "lower(email)"},
		{Name: "idx_orders_data", Columns: []string{"data"}, Method: "gin"},
	}}
	p := tablePlan(t, tableSchema(), tableSchema(table))
	for _, sql := range []string{
		`CREATE INDEX IF NOT EXISTS "idx_orders_active" ON "public"."orders" USING "btree" ("email") WHERE active`,
		`CREATE INDEX IF NOT EXISTS "idx_orders_lower_email" ON "public"."orders" USING "btree" (lower(email))`,
		`CREATE INDEX IF NOT EXISTS "idx_orders_data" ON "public"."orders" USING "gin" ("data")`,
	} {
		if !strings.Contains(p.SQL(), sql) {
			t.Fatalf("missing %s in %s", sql, p.SQL())
		}
	}
	if p := tablePlan(t, tableSchema(table), tableSchema(table)); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
	before, err := schemigo.Normalize(tableSchema(table))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := schemigo.Normalize(before)
	after.Tables[0].Indexes[1].Expression = "upper(email)"
	p = tablePlan(t, before, after)
	if !strings.Contains(p.SQL(), `DROP INDEX IF EXISTS "public"."idx_orders_lower_email"`) || !strings.Contains(p.SQL(), "upper(email)") {
		t.Fatal(p.SQL())
	}
}

func TestDiffForeignKey(t *testing.T) {
	parent := simpleTable("z_parent")
	parent.PrimaryKey = []string{"id"}
	child := simpleTable("a_child")
	child.ForeignKeys = []schemigo.ForeignKey{{Name: "parent_fk", Columns: []string{"id"}, ReferencedTable: parent.Name, ReferencedColumns: []string{"id"}, OnDelete: "CASCADE"}}
	p := tablePlan(t, tableSchema(), tableSchema(child, parent))
	sql := p.SQL()
	if !strings.Contains(sql, `ADD CONSTRAINT "parent_fk" FOREIGN KEY ("id") REFERENCES "public"."z_parent" ("id") ON DELETE CASCADE ON UPDATE NO ACTION`) {
		t.Fatal(sql)
	}
	if strings.LastIndex(sql, "CREATE TABLE") > strings.Index(sql, "ADD CONSTRAINT") {
		t.Fatal("FK created before all tables")
	}
	if p := tablePlan(t, tableSchema(child, parent), tableSchema(child, parent)); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
	for _, action := range []string{"CASCADE; DROP TABLE x", "NO ACTION|RESTRICT"} {
		child.ForeignKeys[0].OnDelete = action
		if _, err := postgres.New().Diff(t.Context(), nil, tableSchema(), tableSchema(child, parent)); err == nil {
			t.Fatal("invalid action accepted")
		}
	}
}

func TestDiffForeignKeyEquivalentIndex(t *testing.T) {
	parent := simpleTable("parent")
	parent.PrimaryKey = []string{"id"}
	parent.Indexes = []schemigo.Index{{Name: "idx", Columns: []string{"id"}, Where: "(id > 0)"}}
	child := simpleTable("child")
	child.ForeignKeys = []schemigo.ForeignKey{{Name: "fk", Columns: []string{"id"}, ReferencedTable: "parent", ReferencedColumns: []string{"id"}}}
	current := tableSchema(child, parent)
	desired, _ := schemigo.Normalize(current)
	desired.Tables[1].Indexes[0].Where = "id>0"
	if p := tablePlan(t, current, desired); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
}

func TestDiffRenameTableEquivalentConstraints(t *testing.T) {
	before := simpleTable("old")
	before.Indexes = []schemigo.Index{{Name: "idx", Columns: []string{"id"}, Where: "(id > 0)"}}
	before.Checks = []schemigo.Check{{Name: "checked", Expression: "(id >= 0)"}}
	after := simpleTable("new")
	after.Indexes = []schemigo.Index{{Name: "idx", Columns: []string{"id"}, Where: "id>0"}}
	after.Checks = []schemigo.Check{{Name: "checked", Expression: "id>=0"}}
	p := tablePlan(t, tableSchema(before), tableSchema(after))
	if len(p.Statements) != 1 || !strings.Contains(p.SQL(), `RENAME TO "new"`) || p.Statements[0].Destructive {
		t.Fatal(p.SQL())
	}
}

func TestDiffCheck(t *testing.T) {
	before := simpleTable("orders")
	after := simpleTable("orders")
	after.Checks = []schemigo.Check{{Name: "chk_orders_total", Expression: "id >= 0"}}
	p := tablePlan(t, tableSchema(before), tableSchema(after))
	if !strings.Contains(p.SQL(), `ADD CONSTRAINT "chk_orders_total" CHECK (id >= 0)`) {
		t.Fatal(p.SQL())
	}
	before.Checks = []schemigo.Check{{Name: "chk_orders_total", Expression: "(id >= 0)"}}
	if p := tablePlan(t, tableSchema(before), tableSchema(after)); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
}

func TestDiffIndexNamesAndOptions(t *testing.T) {
	for _, name := range []string{"duplicate", strings.Repeat("x", 64)} {
		a, b := simpleTable("a"), simpleTable("b")
		a.Indexes = []schemigo.Index{{Name: name, Columns: []string{"id"}}}
		b.Indexes = a.Indexes
		if _, err := postgres.New().Diff(t.Context(), nil, tableSchema(), tableSchema(a, b)); err == nil {
			t.Fatalf("invalid schema-wide index name %q accepted", name)
		}
	}
	table := simpleTable("a")
	table.Indexes = []schemigo.Index{{Name: "idx", Columns: []string{"id"}, Type: "minmax", Granularity: 4}}
	if _, err := postgres.New().Diff(t.Context(), nil, tableSchema(), tableSchema(table)); err == nil {
		t.Fatal("ClickHouse index options accepted")
	}
}
