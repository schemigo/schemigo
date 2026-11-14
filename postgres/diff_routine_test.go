package postgres_test

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/postgres"
)

func TestTriggerOnNewTableAllowsPlanReplay(t *testing.T) {
	s := tableSchema(simpleTable("users"))
	s.Functions = []schemigo.Function{{Name: "touch", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END"}}
	s.Triggers = []schemigo.Trigger{{Name: "touch", Table: "users", When: "BEFORE", Event: "UPDATE", Function: "touch"}}
	p := tablePlan(t, tableSchema(), s)
	// The replay guard must permit the trigger that this same plan creates,
	// while still rejecting triggers absent from the desired schema.
	if !strings.Contains(p.Statements[0].SQL, "tgname NOT IN") || !strings.Contains(p.Statements[0].SQL, "touch") {
		t.Fatal("CREATE TABLE replay would reject its own managed trigger")
	}
}

func TestFunctionCreateAndChange(t *testing.T) {
	empty := schemigo.Schema{Name: "public"}
	desired := schemigo.Schema{Name: "public", Functions: []schemigo.Function{{Name: `echo"`, Parameters: "value text DEFAULT 'a,b'", Returns: "text", Language: "sql", Body: "SELECT value || '$schemigo$';"}}}
	p := tablePlan(t, empty, desired)
	for _, want := range []string{`CREATE OR REPLACE FUNCTION "public"."echo"""(value text DEFAULT ''a,b'')`, `RETURNS text`, `LANGUAGE "sql"`, "$schemigo_1$SELECT value || ''$schemigo$'';$schemigo_1$"} {
		if !strings.Contains(p.SQL(), want) {
			t.Fatalf("missing %q: %s", want, p.SQL())
		}
	}
	if p := tablePlan(t, desired, desired); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
	changed, _ := schemigo.Normalize(desired)
	changed.Functions[0].Body = "SELECT upper(value);"
	p = tablePlan(t, desired, changed)
	if strings.Contains(p.SQL(), "DROP FUNCTION") || !strings.Contains(p.SQL(), "upper(value)") {
		t.Fatal(p.SQL())
	}
}

func TestFunctionSignatureAndDrop(t *testing.T) {
	current := schemigo.Schema{Name: "public", Functions: []schemigo.Function{
		{Name: "echo", Parameters: "value int8 DEFAULT 1", Returns: "bigint", Language: "sql", Body: "SELECT value;"},
		{Name: "echo", Parameters: "value text", Returns: "text", Language: "sql", Body: "SELECT value;"},
	}}
	desired, _ := schemigo.Normalize(current)
	desired.Functions[0].Parameters = "value bigint DEFAULT 1"
	if p := tablePlan(t, current, desired); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
	desired.Functions = desired.Functions[1:]
	p := tablePlan(t, current, desired)
	if !strings.Contains(p.SQL(), `DROP FUNCTION IF EXISTS "public"."echo"(bigint)`) || strings.Contains(p.SQL(), "DEFAULT") || strings.Contains(p.SQL(), "CASCADE") {
		t.Fatal(p.SQL())
	}
}

func TestFunctionReturnChangeNeedsDependencies(t *testing.T) {
	current := schemigo.Schema{Name: "public", Functions: []schemigo.Function{{Name: "f", Returns: "integer", Language: "sql", Body: "SELECT 1;"}}}
	desired, _ := schemigo.Normalize(current)
	desired.Functions[0].Returns = "bigint"
	if p, err := postgres.New().Diff(t.Context(), nil, current, desired); err == nil || len(p.Statements) != 0 {
		t.Fatalf("unsafe replacement: %+v, %v", p, err)
	}
}

func TestTriggerCreateChangeAndOrder(t *testing.T) {
	desired := schemigo.Schema{Name: "public", Order: []schemigo.EntityKind{schemigo.EntityTriggers, schemigo.EntityViews},
		Tables:    []schemigo.Table{simpleTable("users")},
		Functions: []schemigo.Function{{Name: "touch", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END"}},
		Triggers:  []schemigo.Trigger{{Name: "touch", Table: "users", When: "BEFORE", Event: "UPDATE OR INSERT", Function: "touch", Condition: "NEW.id > 0"}},
	}
	p := tablePlan(t, tableSchema(), desired)
	sql := p.SQL()
	if !strings.Contains(sql, `CREATE OR REPLACE TRIGGER "touch" BEFORE INSERT OR UPDATE ON "public"."users" FOR EACH ROW WHEN (NEW.id > 0) EXECUTE FUNCTION "public"."touch"()`) {
		t.Fatal(sql)
	}
	if strings.Index(sql, "CREATE OR REPLACE TRIGGER") < strings.Index(sql, "CREATE OR REPLACE FUNCTION") || strings.Index(sql, "CREATE OR REPLACE TRIGGER") < strings.Index(sql, "CREATE TABLE") {
		t.Fatal("trigger before dependency")
	}
	current, _ := schemigo.Normalize(desired)
	current.Triggers[0].Event = "INSERT OR UPDATE"
	if p := tablePlan(t, current, desired); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
	desired.Triggers[0].When = "AFTER"
	if p := tablePlan(t, current, desired); !strings.Contains(p.SQL(), `"touch" AFTER`) {
		t.Fatal(p.SQL())
	}
	desired.Triggers = nil
	desired.Functions = nil
	p = tablePlan(t, current, desired)
	if strings.Index(p.SQL(), "DROP TRIGGER") > strings.Index(p.SQL(), "DROP FUNCTION") {
		t.Fatal(p.SQL())
	}
}

func TestTriggerRejectsInvalidClauses(t *testing.T) {
	for _, event := range []string{"UPDATE; DROP TABLE users", "INSERT OR INSERT", "TRUNCATE"} {
		s := tableSchema(simpleTable("users"))
		s.Functions = []schemigo.Function{{Name: "f", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END"}}
		s.Triggers = []schemigo.Trigger{{Name: "t", Table: "users", Function: "f", When: "BEFORE", Event: event}}
		if _, err := postgres.New().Diff(t.Context(), nil, tableSchema(), s); err == nil {
			t.Fatalf("accepted %q", event)
		}
	}
}

func TestViewReferencesPlannedTable(t *testing.T) {
	desired := tableSchema(simpleTable("users"))
	desired.Views = []schemigo.View{{Name: "z_base", Definition: "SELECT id FROM users"}, {Name: "a_dependent", Definition: "SELECT id FROM z_base"}}
	desired.Order = []schemigo.EntityKind{schemigo.EntityViews}
	p := tablePlan(t, tableSchema(), desired)
	if strings.Index(p.SQL(), `VIEW "public"."z_base"`) > strings.Index(p.SQL(), `VIEW "public"."a_dependent"`) || strings.Index(p.SQL(), "CREATE TABLE") > strings.Index(p.SQL(), "CREATE OR REPLACE VIEW") {
		t.Fatal(p.SQL())
	}
}

func TestTriggerSurvivesTableRename(t *testing.T) {
	current := tableSchema(simpleTable("old"))
	current.Functions = []schemigo.Function{{Name: "touch", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END"}}
	current.Triggers = []schemigo.Trigger{{Name: "touch", Table: "old", Function: "touch", When: "BEFORE", Event: "UPDATE"}}
	desired, _ := schemigo.Normalize(current)
	desired.Tables[0].Name = "new"
	desired.Triggers[0].Table = "new"
	p := tablePlan(t, current, desired)
	if strings.Contains(p.SQL(), "DROP TRIGGER") && !strings.Contains(p.SQL(), "CREATE OR REPLACE TRIGGER") {
		t.Fatal("rename loses trigger", p.SQL())
	}
}
func TestFunctionTypeModifiersAreNotIdentity(t *testing.T) {
	current := schemigo.Schema{Name: "public", Functions: []schemigo.Function{{Name: "f", Parameters: "x numeric", Returns: "numeric", Language: "sql", Body: "SELECT x;"}}}
	desired, _ := schemigo.Normalize(current)
	desired.Functions[0].Parameters = "x numeric(10,2)"
	if p := tablePlan(t, current, desired); len(p.Statements) != 0 {
		t.Fatal("type modifiers change function identity", p.SQL())
	}
}
func TestViewDefinitionComparison(t *testing.T) {
	a := schemigo.Schema{Name: "public", Views: []schemigo.View{{Name: "v", Definition: "SELECT 'a b' AS value;"}}}
	b, _ := schemigo.Normalize(a)
	b.Views[0].Definition = "select  'a b'  as value"
	if p := tablePlan(t, a, b); len(p.Statements) != 0 {
		t.Fatal(p.SQL())
	}
	b.Views[0].Definition = "SELECT 'ab' AS value"
	if _, err := postgres.New().Diff(t.Context(), nil, a, b); err == nil {
		t.Fatal("changed literal accepted without catalog")
	}
}
