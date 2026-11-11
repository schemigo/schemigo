//go:build integration

package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/internal/testdb"
)

func TestIndexMigration(t *testing.T) {
	db := testdb.Postgres(t)
	desired := tableSchema(schemigo.Table{Name: "orders", Columns: []schemigo.Column{
		{Name: "id", Type: "bigint"}, {Name: "email", Type: "text"}, {Name: "active", Type: "boolean"}, {Name: "data", Type: "jsonb"},
	}, Indexes: []schemigo.Index{
		{Name: "idx_orders_active", Columns: []string{"email"}, Where: "active"},
		{Name: "idx_orders_lower_email", Expression: "lower(email)"},
		{Name: "idx_orders_data", Columns: []string{"data"}, Method: "gin"},
		{Name: "idx_orders_id", Columns: []string{"id"}, Unique: true},
	}})
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	execFixture(t, db, `INSERT INTO public.orders VALUES (1,'a',true,'{}')`)
	desired.Tables[0].Indexes[0].Where = "(NOT active)"
	desired.Tables[0].Indexes[1].Expression = "upper(email)"
	desired.Tables[0].Indexes[3].Unique = false
	desired.Tables[0].Indexes[3].Method = "hash"
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired.Tables[0].Indexes = nil
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
}

func TestForeignKeyMigration(t *testing.T) {
	db := testdb.Postgres(t)
	parent := schemigo.Table{Name: "z_parent", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}, {Name: "code", Type: "bigint"}}, PrimaryKey: []string{"id"}}
	child := simpleTable("a_child")
	child.ForeignKeys = []schemigo.ForeignKey{{Name: "parent_fk", Columns: []string{"id"}, ReferencedTable: parent.Name, ReferencedColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "CASCADE"}}
	desired := tableSchema(child, parent)
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	_, err := db.ExecContext(t.Context(), `INSERT INTO public.a_child VALUES (99)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("FK not enforced: %v", err)
	}
	execFixture(t, db, `INSERT INTO public.z_parent VALUES (1,11); INSERT INTO public.a_child VALUES (1); UPDATE public.z_parent SET id=2 WHERE id=1`)
	var value int
	if err := db.QueryRowContext(t.Context(), `SELECT id FROM public.a_child`).Scan(&value); err != nil || value != 2 {
		t.Fatalf("ON UPDATE CASCADE value=%d err=%v", value, err)
	}
	desired.Tables[0].ForeignKeys[0].OnDelete = "SET NULL"
	desired.Tables[0].Columns[0].Type = "integer"
	desired.Tables[1].Columns[0].Type = "integer"
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired.Tables[1].PrimaryKey = []string{"code"}
	desired.Tables[1].Columns[0].NotNull = true
	desired.Tables[1].Indexes = []schemigo.Index{{Name: "parent_id", Columns: []string{"id"}, Unique: true}}
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	execFixture(t, db, `DELETE FROM public.z_parent`)
	var nulls int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM public.a_child WHERE id IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("ON DELETE SET NULL nulls=%d err=%v", nulls, err)
	}
}

func TestForeignKeyUniqueIndexReplacement(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.parent(id bigint); CREATE UNIQUE INDEX old_key ON public.parent(id); CREATE TABLE public.child(id bigint CONSTRAINT fk REFERENCES public.parent(id)); INSERT INTO public.parent VALUES(1); INSERT INTO public.child VALUES(1)`)
	desired := inspect(t, db, "public")
	for i := range desired.Tables {
		if desired.Tables[i].Name == "parent" {
			desired.Tables[i].Indexes[0].Name = "new_key"
		}
	}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	var index string
	if err := db.QueryRowContext(t.Context(), `SELECT conindid::regclass::text FROM pg_constraint WHERE conname='fk' AND conrelid='public.child'::regclass`).Scan(&index); err != nil || index != "new_key" {
		t.Fatalf("supporting index=%q err=%v", index, err)
	}
}

func TestCheckMigration(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.orders(id bigint); INSERT INTO public.orders VALUES(1)`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Checks = []schemigo.Check{{Name: "chk_orders_total", Expression: "id >= 0"}}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	_, err := db.ExecContext(t.Context(), `INSERT INTO public.orders VALUES(-1)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("CHECK not enforced: %v", err)
	}
	desired.Tables[0].Checks[0].Expression = "id > 0"
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	desired.Tables[0].Checks = nil
	p = diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
}

func TestCheckValidationFailureIsAtomic(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t(id bigint); INSERT INTO public.t VALUES(-1)`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Checks = []schemigo.Check{{Name: "positive", Expression: "id >= 0"}}
	p := diffFromDB(t, db, desired)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("invalid data accepted")
	}
	if len(inspect(t, db, "public").Tables[0].Checks) != 0 {
		t.Fatal("failed validation left an unvalidated CHECK")
	}
}

func TestForeignKeyCycleDrop(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.a(id bigint PRIMARY KEY, ref bigint); CREATE TABLE public.b(id bigint PRIMARY KEY, ref bigint REFERENCES public.a(id)); ALTER TABLE public.a ADD CONSTRAINT fk FOREIGN KEY(ref) REFERENCES public.b(id)`)
	p := diffFromDB(t, db, tableSchema())
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, tableSchema())
}

func TestConstraintColumnDrop(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.parent(id bigint PRIMARY KEY); CREATE TABLE public.child(id bigint, ref bigint REFERENCES public.parent(id), CONSTRAINT positive CHECK(ref > 0)); CREATE INDEX lookup ON public.child(ref)`)
	desired := inspect(t, db, "public")
	for i := range desired.Tables {
		if desired.Tables[i].Name == "child" {
			desired.Tables[i].Columns = desired.Tables[i].Columns[:1]
			desired.Tables[i].Indexes = nil
			desired.Tables[i].Checks = nil
			desired.Tables[i].ForeignKeys = nil
		}
	}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
}

func TestIndexAndCheckConflictGuards(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t(id bigint); CREATE TABLE public.other(id bigint)`)
	desired := inspect(t, db, "public")
	for i := range desired.Tables {
		if desired.Tables[i].Name == "t" {
			desired.Tables[i].Indexes = []schemigo.Index{{Name: "lookup", Columns: []string{"id"}}}
		}
	}
	p := diffFromDB(t, db, desired)
	execFixture(t, db, `CREATE INDEX lookup ON public.other(id)`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("index belonging to another table accepted")
	}
	desired = inspect(t, db, "public")
	for i := range desired.Tables {
		if desired.Tables[i].Name == "t" {
			desired.Tables[i].Checks = []schemigo.Check{{Name: "positive", Expression: "id > 0"}}
		}
	}
	p = diffFromDB(t, db, desired)
	execFixture(t, db, `ALTER TABLE public.t ADD CONSTRAINT positive CHECK(id < 0)`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("conflicting CHECK accepted")
	}
}

func TestCheckWordBoundaryGuard(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t(foo boolean, bar boolean, fooandbar boolean)`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Checks = []schemigo.Check{{Name: "checked", Expression: "foo AND bar"}}
	p := diffFromDB(t, db, desired)
	execFixture(t, db, `ALTER TABLE public.t ADD CONSTRAINT checked CHECK(fooANDbar)`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("CHECK over a different identifier accepted")
	}
}

func TestConstraintQuotedSchemaAndCompositeKey(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE SCHEMA "a.b"`)
	desired := schemigo.Schema{Name: "a.b", Order: []schemigo.EntityKind{schemigo.EntityTables}, Tables: []schemigo.Table{
		{Name: `Parent"`, Columns: []schemigo.Column{{Name: "a", Type: "bigint"}, {Name: "b", Type: "bigint"}, {Name: "Name", Type: "text"}}, PrimaryKey: []string{"a", "b"}, Indexes: []schemigo.Index{{Name: `idx"%`, Expression: `lower("Name")`}}, Checks: []schemigo.Check{{Name: `chk"%`, Expression: `"Name" <> 'a b'::text`}}},
		{Name: "child", Columns: []schemigo.Column{{Name: "a", Type: "bigint"}, {Name: "b", Type: "bigint"}}, ForeignKeys: []schemigo.ForeignKey{{Name: `fk"%`, Columns: []string{"a", "b"}, ReferencedTable: `Parent"`, ReferencedColumns: []string{"b", "a"}}}},
	}}
	p := diffFromDB(t, db, desired)
	executePlan(t, db, p)
	executePlan(t, db, p)
	assertConverged(t, db, desired)
	execFixture(t, db, `INSERT INTO "a.b"."Parent""" VALUES(1,2,'valid'); INSERT INTO "a.b".child VALUES(2,1)`)
}

func TestIndexExternalDependencyRefused(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.parent(id bigint); CREATE UNIQUE INDEX parent_key ON public.parent(id)`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Indexes = nil
	p := diffFromDB(t, db, desired)
	execFixture(t, db, `CREATE SCHEMA external; CREATE TABLE external.child(id bigint REFERENCES public.parent(id))`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("unknown FK dependency removed")
	}
	if len(inspect(t, db, "public").Tables[0].Indexes) != 1 {
		t.Fatal("failed DROP removed supporting index")
	}
}

func TestConstraintDropDriftRefused(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TABLE public.t(id bigint CONSTRAINT checked CHECK(id > 0))`)
	desired := inspect(t, db, "public")
	desired.Tables[0].Checks = nil
	p := diffFromDB(t, db, desired)
	execFixture(t, db, `ALTER TABLE public.t DROP CONSTRAINT checked; ALTER TABLE public.t ADD CONSTRAINT checked CHECK(id < 0)`)
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("changed CHECK removed by stale plan")
	}
}
