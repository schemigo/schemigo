//go:build integration

package postgres_test

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/internal/testdb"
	"github.com/schemigo/schemigo/postgres"
)

func diffFromDB(t *testing.T, db *sql.DB, desired schemigo.Schema) schemigo.Plan {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	current, err := postgres.New().Inspect(t.Context(), conn, desired.Name)
	if err != nil {
		t.Fatal(err)
	}
	p, err := postgres.New().Diff(t.Context(), conn, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func assertConverged(t *testing.T, db *sql.DB, desired schemigo.Schema) {
	t.Helper()
	if p := diffFromDB(t, db, desired); len(p.Statements) != 0 {
		t.Fatalf("repeated diff is not empty:\n%s", p.SQL())
	}
}

func TestTableMigration(t *testing.T) {
	db := testdb.Postgres(t)
	desired := tableSchema(schemigo.Table{Name: `User"Archive`, Columns: []schemigo.Column{
		{Name: "order", Type: "int8", PrimaryKey: true}, {Name: "name", Type: "varchar(255)", Default: "'new'"},
	}})
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	execFixture(t, db, `INSERT INTO public."User""Archive" ("order") VALUES (1)`)
	desired.Tables[0].Columns = append(desired.Tables[0].Columns, schemigo.Column{Name: "extra", Type: "text", Default: "'added'"})
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var value string
	if err := db.QueryRowContext(t.Context(), `SELECT extra FROM public."User""Archive" WHERE "order"=1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "added" {
		t.Fatalf("ADD default=%q", value)
	}
	desired.Tables[0].Columns = desired.Tables[0].Columns[:2]
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired.Tables = nil
	p = diffFromDB(t, db, desired)
	if len(p.Statements) != 1 || !p.Statements[0].Destructive {
		t.Fatal("unmarked table drop")
	}
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
}

func TestColumnMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t (value text DEFAULT '1'); INSERT INTO public.t VALUES ('41')`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Columns[0] = schemigo.Column{Name: "value", Type: "int8", Using: `"value"::bigint + 1`, Default: "0", NotNull: true}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var value int64
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM public.t`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 42 {
		t.Fatalf("USING repeated or data lost: %d", value)
	}
	desired.Tables[0].Columns[0].Default = ""
	desired.Tables[0].Columns[0].NotNull = false
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	execFixture(t, db, `INSERT INTO public.t DEFAULT VALUES`)
	var nulls int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM public.t WHERE value IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Fatal("DROP DEFAULT / DROP NOT NULL not applied")
	}
}

func TestColumnTypeFailureIsAtomic(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t (value text DEFAULT 'bad'); INSERT INTO public.t VALUES ('not a number')`)
	before := inspect(t, db, "public")
	desired, err := schemigo.Normalize(before)
	if err != nil {
		t.Fatal(err)
	}
	desired.Tables[0].Columns[0].Type = "bigint"
	desired.Tables[0].Columns[0].Using = `value::bigint`
	desired.Tables[0].Columns[0].Default = "0"
	p := diffFromDB(t, db, desired)
	if len(p.Statements) != 1 {
		t.Fatalf("non-atomic type/default change: %+v", p)
	}
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("invalid cast accepted")
	}
	after := inspect(t, db, "public")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed cast changed schema/default: before=%+v after=%+v", before, after)
	}
	var value string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM public.t`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "not a number" {
		t.Fatal("failed cast changed data")
	}
	execFixture(t, db, `ALTER TABLE public.t ALTER COLUMN value DROP DEFAULT; ALTER TABLE public.t ALTER COLUMN value TYPE integer USING 17`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil || !strings.Contains(err.Error(), "changed since planning") {
		t.Fatalf("stale source type accepted: %v", err)
	}
	var actualType string
	if err := db.QueryRowContext(t.Context(), `SELECT value::text, pg_typeof(value)::text FROM public.t`).Scan(&value, &actualType); err != nil {
		t.Fatal(err)
	}
	if value != "17" || actualType != "integer" {
		t.Fatalf("stale plan changed data/type: %s %s", value, actualType)
	}
}

func TestRenameMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.old (id bigint PRIMARY KEY); CREATE INDEX lookup ON public.old(id);
 CREATE TABLE public.child (parent_id bigint REFERENCES public.old(id));
 CREATE FUNCTION public.touch() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;
 CREATE TRIGGER touch BEFORE UPDATE ON public.old FOR EACH ROW EXECUTE FUNCTION public.touch();
 CREATE TABLE public.oa (id bigint);
 CREATE TRIGGER touch BEFORE UPDATE ON public.oa FOR EACH ROW EXECUTE FUNCTION public.touch();
 INSERT INTO public.old VALUES (17); INSERT INTO public.child VALUES (17)`)
	desired := inspect(t, db, "public")
	for i := range desired.Tables {
		if desired.Tables[i].Name == "old" {
			desired.Tables[i].Name = "new"
		}
		for j := range desired.Tables[i].ForeignKeys {
			if desired.Tables[i].ForeignKeys[j].ReferencedTable == "old" {
				desired.Tables[i].ForeignKeys[j].ReferencedTable = "new"
			}
		}
	}
	for i := range desired.Triggers {
		if desired.Triggers[i].Table == "old" {
			desired.Triggers[i].Table = "new"
		}
	}
	p := diffFromDB(t, db, desired)
	if len(p.Statements) != 1 || strings.Contains(p.SQL(), "DROP TABLE") {
		t.Fatalf("table rename=%+v", p)
	}
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired = inspect(t, db, "public")
	for i := range desired.Tables {
		if desired.Tables[i].Name == "new" {
			desired.Tables[i].Columns[0].Name = "new_id"
			desired.Tables[i].PrimaryKey = []string{"new_id"}
			desired.Tables[i].Indexes[0].Columns = []string{"new_id"}
		}
		for j := range desired.Tables[i].ForeignKeys {
			desired.Tables[i].ForeignKeys[j].ReferencedColumns = []string{"new_id"}
		}
	}
	p = diffFromDB(t, db, desired)
	if len(p.Statements) != 1 || strings.Contains(p.SQL(), "DROP COLUMN") {
		t.Fatalf("column rename=%+v", p)
	}
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var value int
	if err := db.QueryRowContext(t.Context(), `SELECT new_id FROM public.new`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 17 {
		t.Fatalf("rename lost data: %d", value)
	}
}

func TestPrimaryKeyMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t (id bigint, "alt%s" bigint, CONSTRAINT "custom%I'key" PRIMARY KEY(id)); INSERT INTO public.t VALUES (1,11)`)
	desired := inspect(t, db, "public")
	desired.Tables[0].PrimaryKey = []string{"alt%s"}
	desired.Tables[0].Columns[0].NotNull = false
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var name string
	if err := db.QueryRowContext(t.Context(), `SELECT conname FROM pg_constraint WHERE conrelid='public.t'::regclass AND contype='p'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "custom%I'key" {
		t.Fatalf("constraint name lost: %q", name)
	}
	execFixture(t, db, `INSERT INTO public.t VALUES (NULL,12)`)
	desired = inspect(t, db, "public")
	desired.Tables[0].PrimaryKey = nil
	desired.Tables[0].Columns[1].NotNull = false
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	execFixture(t, db, `INSERT INTO public.t VALUES (NULL,NULL)`)
}

func TestColumnDropDependencyGuard(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t (id bigint, value text); INSERT INTO public.t VALUES (1,'keep')`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Columns = desired.Tables[0].Columns[:1]
	p := diffFromDB(t, db, desired)
	execFixture(t, db, `CREATE INDEX obstacle ON public.t(value)`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("DROP COLUMN silently removed a new index")
	}
	var value string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM public.t`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "keep" {
		t.Fatal("guard failed to preserve data")
	}
}

func TestTableEnumDependencyOrder(t *testing.T) {
	db := testdb.Postgres(t)
	desired := tableSchema(schemigo.Table{Name: "t", Columns: []schemigo.Column{{Name: "state", Type: "status", Default: "'a'"}}})
	desired.Enums = []schemigo.Enum{{Name: "status", Values: []string{"a", "b"}}}
	desired.Order = []schemigo.EntityKind{schemigo.EntityTables, schemigo.EntityEnums}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired.Tables = nil
	desired.Enums = nil
	p = diffFromDB(t, db, desired)
	if len(p.Statements) != 2 || !strings.HasPrefix(p.Statements[0].SQL, "DROP TABLE") {
		t.Fatalf("enum dropped before its table: %s", p.SQL())
	}
	executePlan(t, db, p)
	assertConverged(t, db, desired)
}

func TestColumnPrimaryKeyDrop(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t (id bigint PRIMARY KEY, value text); INSERT INTO public.t VALUES (1,'keep')`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Columns = desired.Tables[0].Columns[1:]
	desired.Tables[0].PrimaryKey = nil
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var value string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM public.t`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "keep" {
		t.Fatal("PK column removal lost surviving data")
	}
}

func TestRenameSelfForeignKey(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.node (id bigint PRIMARY KEY, parent bigint REFERENCES public.node(id)); INSERT INTO public.node VALUES (1,NULL),(2,1)`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Name = "renamed"
	desired.Tables[0].ForeignKeys[0].ReferencedTable = "renamed"
	p := diffFromDB(t, db, desired)
	if len(p.Statements) != 1 {
		t.Fatal("not a pure self-FK rename")
	}
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
}

func TestRenameReplayGuards(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.old (id bigint); INSERT INTO public.old VALUES(17)`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Name = "new"
	p := diffFromDB(t, db, desired)
	execFixture(t, db, `DROP TABLE public.old; CREATE TABLE public.old(payload text); INSERT INTO public.old VALUES('keep')`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("renamed an incompatible replacement source")
	}
	execFixture(t, db, `DROP TABLE public.old; CREATE TABLE public.old(id bigint); INSERT INTO public.old VALUES(17)`)
	executePlan(t, db, p)
	execFixture(t, db, `ALTER TABLE public.new ALTER COLUMN id TYPE text USING 'keep'`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("accepted incompatible replay target")
	}
	execFixture(t, db, `CREATE TABLE public.c(a bigint); INSERT INTO public.c VALUES(17)`)
	desired = inspect(t, db, "public")
	for i := range desired.Tables {
		if desired.Tables[i].Name == "c" {
			desired.Tables[i].Columns[0].Name = "b"
		}
	}
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	execFixture(t, db, `ALTER TABLE public.c ALTER COLUMN b TYPE text USING 'keep'`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("accepted incompatible column replay target")
	}
	execFixture(t, db, `DROP TABLE public.c; CREATE TABLE public.c(a bigint)`)
	desired = inspect(t, db, "public")
	for i := range desired.Tables {
		if desired.Tables[i].Name == "c" {
			desired.Tables[i].Columns[0].Name = "b"
		}
	}
	p = diffFromDB(t, db, desired)
	execFixture(t, db, `DROP TABLE public.c; CREATE VIEW public.c AS SELECT 17::bigint AS a`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("renamed a view column using a table plan")
	}
}

func TestTableManagedSchemaScope(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE SCHEMA "a.b"`)
	desired := schemigo.Schema{Name: "a.b", Enums: []schemigo.Enum{{Name: "status", Values: []string{"a"}}}, Tables: []schemigo.Table{{Name: "t", Columns: []schemigo.Column{{Name: "state", Type: "status", Default: "'a'"}, {Name: "stamp", Type: "timestamp(3) with time zone", Default: "now()"}}}}}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	execFixture(t, db, `INSERT INTO "a.b".t DEFAULT VALUES`)
}

func TestRenameDependentViewRefused(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t(value bigint); INSERT INTO public.t VALUES (17); CREATE VIEW public.v AS SELECT value FROM public.t`)
	before := inspect(t, db, "public")
	desired, err := schemigo.Normalize(before)
	if err != nil {
		t.Fatal(err)
	}
	desired.Tables[0].Name = "renamed"
	p := diffFromDB(t, db, desired)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("table rename ignored dependent raw view definition")
	}
	if !reflect.DeepEqual(before, inspect(t, db, "public")) {
		t.Fatal("refused rename changed schema")
	}
	desired, err = schemigo.Normalize(before)
	if err != nil {
		t.Fatal(err)
	}
	desired.Tables[0].Columns[0].Name = "renamed"
	p = diffFromDB(t, db, desired)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("column rename ignored dependent raw view definition")
	}
	if !reflect.DeepEqual(before, inspect(t, db, "public")) {
		t.Fatal("refused rename changed schema")
	}
}

func TestTableDropForeignKeyOrder(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.parent(id bigint PRIMARY KEY); CREATE TABLE public.z_child(id bigint REFERENCES public.parent(id)); INSERT INTO public.parent VALUES(1); INSERT INTO public.z_child VALUES(1)`)
	p := diffFromDB(t, db, tableSchema())
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, tableSchema())
}

func TestTableAndColumnConflicts(t *testing.T) {
	db := testdb.Postgres(t)
	desired := tableSchema(simpleTable("t"))
	p := diffFromDB(t, db, desired)
	execFixture(t, db, `CREATE TABLE public.t (id text); INSERT INTO public.t VALUES('keep')`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("CREATE accepted incompatible existing table")
	}
	execFixture(t, db, `DROP TABLE public.t; CREATE TABLE public.t(id bigint)`)
	desired.Tables[0].Columns = append(desired.Tables[0].Columns, schemigo.Column{Name: "extra", Type: "text"})
	p = diffFromDB(t, db, desired)
	execFixture(t, db, `ALTER TABLE public.t ADD COLUMN extra bigint; INSERT INTO public.t VALUES(1,17)`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("ADD accepted incompatible existing column")
	}
	var extra int
	if err := db.QueryRowContext(t.Context(), `SELECT extra FROM public.t`).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	if extra != 17 {
		t.Fatal("conflict guard changed data")
	}
}
