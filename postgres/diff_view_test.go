package postgres

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/schemigo/schemigo"
	"strings"
	"testing"
)

func TestFunctionDefaultRemovalRequiresRebuild(t *testing.T) {
	old := schemigo.Function{Name: "f", Parameters: "x integer DEFAULT 1", Returns: "numeric", Language: "sql", Body: "SELECT x"}
	next := old
	next.Parameters = "x integer"
	if !functionNeedsDrop(functionChange{old: &old, next: &next}) {
		t.Fatal("default removal must rebuild")
	}
}
func TestFunctionReturnModifiersCompareEqual(t *testing.T) {
	old := schemigo.Schema{Name: "public", Functions: []schemigo.Function{{Name: "f", Returns: "numeric", Language: "sql", Body: "SELECT 1"}}}
	next, _ := schemigo.Normalize(old)
	next.Functions[0].Returns = "numeric(10,2)"
	changes, err := functionChanges(old, next)
	if err != nil || len(changes) != 0 {
		t.Fatalf("type modifier is not stored by PostgreSQL: %+v %v", changes, err)
	}
}
func TestViewRebuildForChangedTableColumn(t *testing.T) {
	current, desired := viewPair()
	desired.Views = current.Views
	current.Tables = []schemigo.Table{{Name: "t", Columns: []schemigo.Column{{Name: "id", Type: "integer"}}}}
	desired.Tables = []schemigo.Table{{Name: "t", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}}}}
	p, err := planViews(current, desired, nil, viewFixture{deps: map[string][]routineDependent{"table:t": {{Kind: "view", Schema: "public", Name: "base"}}}})
	if err != nil || len(p.drops) != 1 || len(p.creates) != 1 {
		t.Fatalf("unchanged view not rebuilt: %+v %v", p, err)
	}
}
func TestFunctionCreationDependencyOrder(t *testing.T) {
	s := schemigo.Schema{Name: "public", Functions: []schemigo.Function{
		{Name: "a", Returns: "integer", Language: "sql", Body: "SELECT z();"},
		{Name: "z", Returns: "integer", Language: "sql", Body: "SELECT 1;"},
	}}
	p, err := New().Diff(t.Context(), nil, schemigo.Schema{Name: "public"}, s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(p.SQL(), `FUNCTION "public"."z"`) > strings.Index(p.SQL(), `FUNCTION "public"."a"`) {
		t.Fatal("function created before its dependency")
	}
}

func TestViewDiffReadOnlySnapshotFailure(t *testing.T) {
	cause := errors.New("catalog unavailable")
	c := &catalogConn{values: []driver.Value{"pg_catalog"}, failAt: 2, cause: cause}
	db := sql.OpenDB(catalogConnector{conn: c})
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	current, desired := viewPair()
	current.Name = `a'".schema`
	desired.Name = current.Name
	_, err = New().Diff(t.Context(), conn, current, desired)
	// The second catalog query intentionally fails. No statement is executed,
	// and the transaction must release its snapshot and local search_path.
	if err == nil || !c.options.ReadOnly || !c.rolledBack || c.committed {
		t.Fatalf("err=%v readOnly=%v rollback=%v", err, c.options.ReadOnly, c.rolledBack)
	}
}

func TestFunctionDefaultCatalogCastIsEquivalent(t *testing.T) {
	current := schemigo.Schema{Name: "public", Functions: []schemigo.Function{{Name: "f", Parameters: "x text DEFAULT 'a,b'::text", Returns: "text", Language: "sql", Body: "SELECT x;"}}}
	desired, _ := schemigo.Normalize(current)
	desired.Functions[0].Parameters = "x text DEFAULT 'a,b'"
	if changes, err := functionChanges(current, desired); err != nil || len(changes) != 0 {
		t.Fatalf("catalog cast creates drift: %+v %v", changes, err)
	}
}
func TestViewRejectsReservedAndLongNames(t *testing.T) {
	for _, name := range []string{"schema_migrations", strings.Repeat("x", 64)} {
		s := schemigo.Schema{Name: "public", Views: []schemigo.View{{Name: name, Definition: "SELECT 1"}}}
		if _, err := planViews(schemigo.Schema{Name: "public"}, s, nil, nil); err == nil {
			t.Fatalf("accepted view %q", name)
		}
	}
}

type viewFixture struct {
	canReplace bool
	deps       map[string][]routineDependent
	err        error
}

func (f viewFixture) compatible(schemigo.View) (bool, error) { return f.canReplace, f.err }
func (f viewFixture) dependents(kind, name, identity string) ([]routineDependent, error) {
	return f.deps[kind+":"+name], f.err
}
func viewPair() (schemigo.Schema, schemigo.Schema) {
	current := schemigo.Schema{Name: "public", Views: []schemigo.View{{Name: "base", Definition: "SELECT 1 AS a"}}}
	desired, _ := schemigo.Normalize(current)
	desired.Views[0].Definition = "SELECT 1 AS a, 2 AS b"
	return current, desired
}
func TestViewAppendColumnsReplaces(t *testing.T) {
	current, desired := viewPair()
	p, err := planViews(current, desired, nil, viewFixture{canReplace: true})
	if err != nil || len(p.drops) != 0 || len(p.creates) != 1 || !strings.Contains(p.creates[0].SQL, `CREATE OR REPLACE VIEW "public"."base"`) {
		t.Fatalf("%+v %v", p, err)
	}
}
func TestViewReorderColumnsRecreates(t *testing.T) {
	current, desired := viewPair()
	current.Views = append(current.Views, schemigo.View{Name: "child", Definition: "SELECT a FROM base"})
	desired.Views = append(desired.Views, current.Views[1])
	desired.Views[0].Definition = "SELECT 2 AS b, 1 AS a"
	fixture := viewFixture{deps: map[string][]routineDependent{"view:base": {{Kind: "view", Schema: "public", Name: "child"}}}}
	p, err := planViews(current, desired, nil, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.drops) != 2 || len(p.creates) != 2 || !strings.Contains(p.drops[0].SQL, `"child"`) || !strings.Contains(p.drops[1].SQL, `"base"`) || !strings.Contains(p.creates[0].SQL, `"base"`) {
		t.Fatalf("%+v", p)
	}
	for _, stmt := range p.drops {
		if strings.Contains(stmt.SQL, "CASCADE") {
			t.Fatal(stmt.SQL)
		}
	}
}
func TestViewMaterializedChangeRecreates(t *testing.T) {
	current, desired := viewPair()
	current.Views[0].Materialized = true
	desired.Views[0].Materialized = true
	p, err := planViews(current, desired, nil, viewFixture{})
	if err != nil || len(p.drops) != 1 || !p.drops[0].Destructive || !strings.Contains(p.creates[0].SQL, "CREATE MATERIALIZED VIEW IF NOT EXISTS") {
		t.Fatalf("%+v %v", p, err)
	}
	desired.Views[0].Materialized = false
	p, err = planViews(current, desired, nil, viewFixture{})
	if err != nil || !strings.Contains(p.drops[0].SQL, "DROP MATERIALIZED VIEW") || !strings.Contains(p.creates[0].SQL, "CREATE OR REPLACE VIEW") {
		t.Fatalf("%+v %v", p, err)
	}
}
func TestViewUnknownDependencyFails(t *testing.T) {
	current, desired := viewPair()
	for _, d := range []routineDependent{{Kind: "view", Schema: "external", Name: "child"}, {Kind: "view", Schema: "public", Name: "unmanaged"}, {Kind: "unknown", Description: "grants"}} {
		_, err := planViews(current, desired, nil, viewFixture{deps: map[string][]routineDependent{"view:base": {d}}})
		if err == nil {
			t.Fatalf("accepted %+v", d)
		}
	}
	cause := errors.New("catalog failed")
	if _, err := planViews(current, desired, nil, viewFixture{err: cause}); !errors.Is(err, cause) {
		t.Fatal(err)
	}
}
func TestViewRestoresTriggerAfterRecreate(t *testing.T) {
	current, desired := viewPair()
	current.Triggers = []schemigo.Trigger{{Name: "edit", Table: "base", Function: "edit"}}
	p, err := planViews(current, desired, nil, viewFixture{deps: map[string][]routineDependent{"view:base": {{Kind: "trigger", Schema: "public", Name: "edit", Table: "base"}}}})
	if err != nil || !p.triggerDrops["base\x00edit"] {
		t.Fatalf("%+v %v", p, err)
	}
}
func TestFunctionRebuildDependentView(t *testing.T) {
	current, desired := viewPair()
	desired.Views = current.Views
	old := schemigo.Function{Name: "f", Returns: "integer", Language: "sql", Body: "SELECT 1"}
	next := old
	next.Returns = "bigint"
	p, err := planViews(current, desired, []functionChange{{old: &old, next: &next}}, viewFixture{deps: map[string][]routineDependent{"function:f": {{Kind: "view", Schema: "public", Name: "base"}}}})
	if err != nil || len(p.drops) != 1 || len(p.creates) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
}
func TestViewCycleFails(t *testing.T) {
	s := schemigo.Schema{Name: "public", Views: []schemigo.View{{Name: "a", Definition: "SELECT * FROM b"}, {Name: "b", Definition: "SELECT * FROM a"}}}
	if _, err := planViews(schemigo.Schema{Name: "public"}, s, nil, nil); err == nil {
		t.Fatal("cycle accepted")
	}
}
func TestFunctionArgumentIdentity(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"p_user_id int8", "bigint"}, {"text, integer", "text, integer"},
		{"IN x numeric(10,2) DEFAULT 1, VARIADIC ys text[]", "numeric, text[]"},
		{`"x,y" text DEFAULT concat('a,b', 'c'), OUT result bigint`, "text"},
		{"x timestamp with time zone", "timestamp with time zone"},
		{"double precision, timestamp without time zone", "double precision, timestamp without time zone"},
	}
	for _, tt := range cases {
		t.Run(tt.raw, func(t *testing.T) {
			got, _, err := functionArguments(schemigo.Schema{}, tt.raw)
			if err != nil || got != tt.want {
				t.Fatalf("%q, %v, want %q", got, err, tt.want)
			}
		})
	}
}
