//go:build integration

package postgres_test

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/internal/testdb"
	"github.com/schemigo/schemigo/postgres"
)

func TestViewColumnGrantMigrationRefused(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE VIEW public.base AS SELECT 1 AS a, 2 AS b; GRANT SELECT(a) ON public.base TO PUBLIC`)
	current := inspect(t, db, "public")
	desired, _ := schemigo.Normalize(current)
	desired.Views[0].Definition = "SELECT 2 AS b, 1 AS a"
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := postgres.New().Diff(t.Context(), conn, current, desired); err == nil {
		t.Fatal("view column privileges would be lost")
	}
}
func TestViewTableTypeMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t(id integer); INSERT INTO public.t VALUES(42); CREATE VIEW public.v AS SELECT id FROM public.t`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Columns[0].Type = "bigint"
	p := diffFromDB(t, db, desired)
	if !strings.Contains(p.SQL(), `DROP VIEW IF EXISTS "public"."v"`) {
		t.Fatal(p.SQL())
	}
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var id int
	if err := db.QueryRowContext(t.Context(), "SELECT id FROM public.v").Scan(&id); err != nil || id != 42 {
		t.Fatalf("%d %v", id, err)
	}
}
func TestFunctionDependencyMigration(t *testing.T) {
	db := testdb.Postgres(t)
	desired := schemigo.Schema{Name: "public", Functions: []schemigo.Function{
		{Name: "a", Returns: "integer", Language: "sql", Body: "SELECT z();"},
		{Name: "z", Returns: "integer", Language: "sql", Body: "SELECT 1;"},
	}}
	executePlan(t, db, diffFromDB(t, db, desired))
	assertConverged(t, db, desired)
}
func TestFunctionDefaultRemovalMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE FUNCTION public.f(x integer DEFAULT 1) RETURNS integer LANGUAGE sql AS 'SELECT x'; CREATE VIEW public.v AS SELECT public.f(1) AS value`)
	desired := inspect(t, db, "public")
	desired.Functions[0].Parameters = "x integer"
	executePlan(t, db, diffFromDB(t, db, desired))
	assertConverged(t, db, desired)
}

func TestTriggerNewTableMigrationReplay(t *testing.T) {
	db := testdb.Postgres(t)
	desired := tableSchema(simpleTable("users"))
	desired.Functions = []schemigo.Function{{Name: "touch", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END"}}
	desired.Triggers = []schemigo.Trigger{{Name: "touch", Table: "users", When: "BEFORE", Event: "UPDATE", Function: "touch"}}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
}

func TestFunctionMigration(t *testing.T) {
	db := testdb.Postgres(t)
	desired := schemigo.Schema{Name: "public", Functions: []schemigo.Function{
		{Name: "echo", Parameters: "value text DEFAULT 'a,b'", Returns: "text", Language: "sql", Body: "SELECT value || '$schemigo$';"},
		{Name: "echo", Parameters: "value bigint", Returns: "bigint", Language: "sql", Body: "SELECT value;"},
	}}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired.Functions[0].Body = "SELECT upper(value);"
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var got string
	if err := db.QueryRowContext(t.Context(), `SELECT public.echo('hello'::text)`).Scan(&got); err != nil || got != "HELLO" {
		t.Fatalf("%q %v", got, err)
	}
	desired.Functions = desired.Functions[:1]
	executePlan(t, db, diffFromDB(t, db, desired))
	assertConverged(t, db, desired)
}

func TestViewMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE VIEW public.base AS SELECT 1 AS a; CREATE VIEW public.child AS SELECT a FROM public.base`)
	desired := inspect(t, db, "public")
	for i := range desired.Views {
		if desired.Views[i].Name == "base" {
			desired.Views[i].Definition = "SELECT 1 AS a, 2 AS b"
		}
	}
	p := diffFromDB(t, db, desired)
	if strings.Contains(p.SQL(), "DROP VIEW") {
		t.Fatal("append must replace", p.SQL())
	}
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	for i := range desired.Views {
		if desired.Views[i].Name == "base" {
			desired.Views[i].Definition = "SELECT 2 AS b, 1 AS a"
		}
	}
	p = diffFromDB(t, db, desired)
	if !strings.Contains(p.SQL(), `DROP VIEW IF EXISTS "public"."child"`) {
		t.Fatal(p.SQL())
	}
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var a int
	if err := db.QueryRowContext(t.Context(), "SELECT a FROM public.child").Scan(&a); err != nil || a != 1 {
		t.Fatalf("%d %v", a, err)
	}
	// A changed typmod cannot use CREATE OR REPLACE even with the same type OID.
	execFixture(t, db, `CREATE VIEW public.typed AS SELECT 'a'::varchar(4) AS value`)
	desired = inspect(t, db, "public")
	for i := range desired.Views {
		if desired.Views[i].Name == "typed" {
			desired.Views[i].Definition = "SELECT 'a'::character varying(8) AS value"
		}
	}
	p = diffFromDB(t, db, desired)
	if !strings.Contains(p.SQL(), `DROP VIEW IF EXISTS "public"."typed"`) {
		t.Fatal(p.SQL())
	}
	executePlan(t, db, p)
	assertConverged(t, db, desired)
}

func TestViewMaterializedMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE MATERIALIZED VIEW public.cached AS SELECT 1 AS value`)
	desired := inspect(t, db, "public")
	desired.Views[0].Definition = "SELECT 2 AS value"
	p := diffFromDB(t, db, desired)
	if !p.Statements[0].Destructive {
		t.Fatal("materialized drop not marked destructive")
	}
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired.Views[0].Materialized = false
	executePlan(t, db, diffFromDB(t, db, desired))
	assertConverged(t, db, desired)
}

func TestViewUnknownDependencyMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE VIEW public.base AS SELECT 1 AS a, 2 AS b; CREATE SCHEMA external; CREATE VIEW external.child AS SELECT a FROM public.base`)
	desired := inspect(t, db, "public")
	desired.Views[0].Definition = "SELECT 2 AS b, 1 AS a"
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	current, err := postgres.New().Inspect(t.Context(), conn, "public")
	if err != nil {
		t.Fatal(err)
	}
	if p, err := postgres.New().Diff(t.Context(), conn, current, desired); err == nil || len(p.Statements) != 0 {
		t.Fatalf("unmanaged dependency accepted: %s %v", p.SQL(), err)
	}
}

func TestTriggerMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.users(id bigint); CREATE VIEW public.editable AS SELECT id FROM public.users`)
	desired := inspect(t, db, "public")
	desired.Functions = []schemigo.Function{{Name: "edit", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END"}}
	desired.Triggers = []schemigo.Trigger{{Name: "edit", Table: "editable", When: "INSTEAD OF", Event: "UPDATE", Function: "edit"}}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired.Views[0].Definition = "SELECT id AS changed FROM public.users"
	executePlan(t, db, diffFromDB(t, db, desired))
	assertConverged(t, db, desired)
	desired.Triggers = nil
	desired.Functions = nil
	desired.Views = nil
	executePlan(t, db, diffFromDB(t, db, desired))
	assertConverged(t, db, desired)
}

func TestDryRunDoesNotExecuteDDL(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE VIEW public.base AS SELECT 1 AS a`)
	current := inspect(t, db, "public")
	desired, _ := schemigo.Normalize(current)
	desired.Views[0].Definition = "SELECT 1 AS a, 2 AS b"
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = conn.QueryRowContext(t.Context(), "SHOW search_path").Scan(&before); err != nil {
		t.Fatal(err)
	}
	p, err := postgres.New().Diff(t.Context(), conn, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRowContext(t.Context(), "SHOW search_path").Scan(&after); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if before != after || len(p.Statements) == 0 {
		t.Fatalf("path=%q/%q plan=%s", before, after, p.SQL())
	}
	if p := diffFromDB(t, db, current); len(p.Statements) != 0 {
		t.Fatal("planning changed database", p.SQL())
	}
	var history bool
	if err := db.QueryRowContext(t.Context(), `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&history); err != nil || history {
		t.Fatalf("history=%v err=%v", history, err)
	}
}
