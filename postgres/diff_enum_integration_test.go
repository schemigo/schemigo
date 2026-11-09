//go:build integration

package postgres_test

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/internal/testdb"
	"github.com/schemigo/schemigo/postgres"
)

func planEnum(t *testing.T, db *sql.DB, values ...string) schemigo.Plan {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	current, err := postgres.New().Inspect(t.Context(), conn, "public")
	if err != nil {
		t.Fatal(err)
	}
	desired, err := schemigo.Normalize(current)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range desired.Enums {
		if desired.Enums[i].Name == "status" {
			desired.Enums[i].Values = values
			found = true
		}
	}
	if !found {
		desired.Enums = append(desired.Enums, schemigo.Enum{Name: "status", Values: values})
	}
	plan, err := postgres.New().Diff(t.Context(), conn, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func executePlan(t *testing.T, db *sql.DB, plan schemigo.Plan) {
	t.Helper()
	for _, s := range plan.Statements {
		if _, err := db.ExecContext(t.Context(), s.SQL); err != nil {
			t.Fatalf("execute plan: %v\n%s", err, s.SQL)
		}
	}
}

func TestEnumMigration(t *testing.T) {
	db := testdb.Postgres(t)
	create := planEnum(t, db, "a", "c")
	execFixture(t, db, `CREATE DOMAIN public.status AS text`)
	if _, err := db.ExecContext(t.Context(), create.Statements[0].SQL); err == nil {
		t.Fatal("existing non-enum type accepted")
	}
	execFixture(t, db, `DROP DOMAIN public.status`)
	executePlan(t, db, create)
	executePlan(t, db, create)
	executePlan(t, db, planEnum(t, db, "a", "b", "c", "d"))
	execFixture(t, db, `CREATE TABLE public.items (id int PRIMARY KEY, state public.status DEFAULT 'a', states public.status[] DEFAULT ARRAY['a']::public.status[]);
 INSERT INTO public.items (id,state,states) VALUES (1,'b',ARRAY['a','d']::public.status[]);`)
	p := planEnum(t, db, "d", "a", "b")
	if len(p.Statements) != 1 || !p.Statements[0].Destructive || !strings.Contains(p.SQL(), "DROP DEFAULT") || !strings.Contains(p.SQL(), "SET DEFAULT") {
		t.Fatalf("rebuild plan=%+v", p)
	}
	executePlan(t, db, p)
	executePlan(t, db, p)
	if again := planEnum(t, db, "d", "a", "b"); len(again.Statements) != 0 {
		t.Fatalf("nonempty repeated diff: %s", again.SQL())
	}
	execFixture(t, db, `INSERT INTO public.items (id) VALUES (2)`)
	var state, states string
	if err := db.QueryRowContext(t.Context(), `SELECT state::text,states::text FROM public.items WHERE id=1`).Scan(&state, &states); err != nil {
		t.Fatal(err)
	}
	if state != "b" || states != "{a,d}" {
		t.Fatalf("data changed: %q, %q", state, states)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT state::text,states::text FROM public.items WHERE id=2`).Scan(&state, &states); err != nil {
		t.Fatal(err)
	}
	if state != "a" || states != "{a}" {
		t.Fatalf("defaults lost: %q, %q", state, states)
	}
}

func TestEnumAddedValueVisibility(t *testing.T) {
	db := testdb.Postgres(t)
	executePlan(t, db, planEnum(t, db, "a"))
	p := planEnum(t, db, "a", "b")
	if !p.Statements[0].OutsideTransaction {
		t.Fatal("missing transaction boundary")
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), p.Statements[0].SQL); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = tx.ExecContext(t.Context(), `SELECT 'b'::public.status`)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "55P04" {
		tx.Rollback()
		t.Fatalf("expected unsafe enum use before commit, got %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	executePlan(t, db, p)
	execFixture(t, db, `SELECT 'b'::public.status`)
	if len(planEnum(t, db, "a", "b").Statements) != 0 {
		t.Fatal("enum addition did not converge")
	}
}

func enumUsedFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TYPE public.status AS ENUM ('a','b'); CREATE TABLE public.items (state public.status DEFAULT 'a'); INSERT INTO public.items VALUES ('b')`)
	return db
}

func TestEnumRemoveUsedValueFails(t *testing.T) {
	db := enumUsedFixture(t)
	before := inspect(t, db, "public")
	p := planEnum(t, db, "a")
	if len(p.Statements) != 1 {
		t.Fatalf("non-atomic rebuild: %+v", p)
	}
	_, err := db.ExecContext(t.Context(), p.Statements[0].SQL)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "22P02" {
		t.Fatalf("expected rejected enum value, got %v", err)
	}
	after := inspect(t, db, "public")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed rebuild changed schema: before=%+v after=%+v", before, after)
	}
	var value string
	var temporaryAbsent bool
	if err := db.QueryRowContext(t.Context(), `SELECT state::text, to_regtype('public.status_new') IS NULL FROM public.items`).Scan(&value, &temporaryAbsent); err != nil {
		t.Fatal(err)
	}
	if value != "b" || !temporaryAbsent {
		t.Fatalf("failed rebuild lost data or left temporary type: %q, %v", value, temporaryAbsent)
	}
}

func TestEnumRebuildRetry(t *testing.T) {
	db := enumUsedFixture(t)
	p := planEnum(t, db, "a")
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("expected failure between CREATE TYPE and RENAME")
	}
	execFixture(t, db, `UPDATE public.items SET state='a'`)
	retry := planEnum(t, db, "a")
	if retry.SQL() != p.SQL() {
		t.Fatal("rollback did not restore a retryable schema")
	}
	execFixture(t, db, `CREATE TYPE public.status_new AS ENUM ('foreign')`)
	if _, err := db.ExecContext(t.Context(), retry.Statements[0].SQL); err == nil {
		t.Fatal("overwrote an unrelated temporary type")
	}
	var label string
	if err := db.QueryRowContext(t.Context(), `SELECT enumlabel FROM pg_enum WHERE enumtypid='public.status_new'::regtype`).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "foreign" {
		t.Fatal("temporary type was changed")
	}
	execFixture(t, db, `DROP TYPE public.status_new`)
	executePlan(t, db, planEnum(t, db, "a"))
	if len(planEnum(t, db, "a").Statements) != 0 {
		t.Fatal("retry did not converge")
	}
}

func TestEnumUnknownDependencies(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TYPE public.status AS ENUM ('a','b'); CREATE TABLE public.items (state public.status); CREATE VIEW public.obstacle AS SELECT state::text FROM public.items`)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	current, err := postgres.New().Inspect(t.Context(), conn, "public")
	if err != nil {
		t.Fatal(err)
	}
	desired, err := schemigo.Normalize(current)
	if err != nil {
		t.Fatal(err)
	}
	desired.Enums[0].Values = []string{"a"}
	p, err := postgres.New().Diff(t.Context(), conn, current, desired)
	if err == nil || !strings.Contains(err.Error(), "obstacle") || len(p.Statements) != 0 {
		t.Fatalf("unknown dependency plan=%+v, err=%v", p, err)
	}
}

func TestExtensionMigration(t *testing.T) {
	db := testdb.Postgres(t)
	var latest, older string
	if err := db.QueryRowContext(t.Context(), `SELECT default_version FROM pg_available_extensions WHERE name='pg_trgm'`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT version FROM pg_available_extension_versions WHERE name='pg_trgm' AND version<>$1 ORDER BY version LIMIT 1`, latest).Scan(&older); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{older, latest} {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		current, err := postgres.New().Inspect(t.Context(), conn, "public")
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		desired := current
		desired.Extensions = []schemigo.Extension{{Name: "pg_trgm", Version: version}}
		p, err := postgres.New().Diff(t.Context(), conn, current, desired)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		executePlan(t, db, p)
		executePlan(t, db, p)
	}
	s := inspect(t, db, "public")
	if len(s.Extensions) != 1 || s.Extensions[0].Version != latest {
		t.Fatalf("extension update failed: %+v", s.Extensions)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	desired := s
	desired.Extensions = nil
	p, err := postgres.New().Diff(t.Context(), conn, s, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("extension removal plan=%+v, error=%v", p, err)
	}
}

func TestEnumQuotedNamesAndValues(t *testing.T) {
	db := testdb.Postgres(t)
	namespace, name, table, column := `a"b.c`, `order"type`, `User"Archive`, `col"value`
	qualified := postgres.Qualified(namespace, name)
	values := []string{"a'b", `back\slash`, "$$; DROP TYPE x;--", "unused"}
	literals := []string{}
	for _, v := range values {
		literals = append(literals, postgres.QuoteLiteral(v))
	}
	execFixture(t, db, "CREATE SCHEMA "+postgres.QuoteIdent(namespace)+"; CREATE TYPE "+qualified+" AS ENUM ("+strings.Join(literals, ", ")+"); CREATE TABLE "+postgres.Qualified(namespace, table)+" ("+postgres.QuoteIdent(column)+" "+qualified+" DEFAULT "+postgres.QuoteLiteral(values[0])+"::"+qualified+"); INSERT INTO "+postgres.Qualified(namespace, table)+" DEFAULT VALUES")
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	current, err := postgres.New().Inspect(t.Context(), conn, namespace)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := schemigo.Normalize(current)
	if err != nil {
		t.Fatal(err)
	}
	desired.Enums[0].Values = []string{values[1], values[0], values[2]}
	p, err := postgres.New().Diff(t.Context(), conn, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	executePlan(t, db, p)
	var value string
	if err := db.QueryRowContext(t.Context(), "SELECT "+postgres.QuoteIdent(column)+"::text FROM "+postgres.Qualified(namespace, table)).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != values[0] {
		t.Fatalf("data changed: %q", value)
	}
	current, err = postgres.New().Inspect(t.Context(), conn, namespace)
	if err != nil {
		t.Fatal(err)
	}
	p, err = postgres.New().Diff(t.Context(), conn, current, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("quoted migration did not converge: %+v, %v", p, err)
	}
	longName := strings.Repeat("x", 63)
	execFixture(t, db, "CREATE TYPE "+postgres.Qualified(namespace, longName)+" AS ENUM ('a','b')")
	current, err = postgres.New().Inspect(t.Context(), conn, namespace)
	if err != nil {
		t.Fatal(err)
	}
	desired, err = schemigo.Normalize(current)
	if err != nil {
		t.Fatal(err)
	}
	for i := range desired.Enums {
		if desired.Enums[i].Name == longName {
			desired.Enums[i].Values = []string{"a"}
		}
	}
	p, err = postgres.New().Diff(t.Context(), conn, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	executePlan(t, db, p)
	current, err = postgres.New().Inspect(t.Context(), conn, namespace)
	if err != nil {
		t.Fatal(err)
	}
	p, err = postgres.New().Diff(t.Context(), conn, current, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("long enum name did not converge: %+v, %v", p, err)
	}
}

func TestEnumMetadata(t *testing.T) {
	db := testdb.Postgres(t)
	fixture := `CREATE TYPE public.status AS ENUM ('a','b'); CREATE TABLE public.items (state public.status DEFAULT 'a'); INSERT INTO public.items DEFAULT VALUES;`
	execFixture(t, db, fixture+` REVOKE USAGE ON TYPE public.status FROM PUBLIC`)
	p := planEnum(t, db, "a")
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("rebuild changed a custom type ACL")
	}
	var allowed bool
	if err := db.QueryRowContext(t.Context(), `SELECT EXISTS (
 SELECT FROM pg_type t CROSS JOIN LATERAL aclexplode(coalesce(t.typacl,acldefault('T',t.typowner))) acl
 WHERE t.oid='public.status'::regtype AND acl.grantee=0 AND acl.privilege_type='USAGE')`).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("PUBLIC regained type USAGE after failed rebuild")
	}
	execFixture(t, db, `DROP TABLE public.items; DROP TYPE public.status;`+fixture+` ALTER DEFAULT PRIVILEGES REVOKE USAGE ON TYPES FROM PUBLIC`)
	p = planEnum(t, db, "a")
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("changed default privileges leaked into existing type")
	}
	execFixture(t, db, `ALTER DEFAULT PRIVILEGES GRANT USAGE ON TYPES TO PUBLIC; COMMENT ON TYPE public.status IS 'keep ''quoted'' comment'`)
	executePlan(t, db, planEnum(t, db, "a"))
	var comment string
	if err := db.QueryRowContext(t.Context(), `SELECT obj_description('public.status'::regtype,'pg_type')`).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	if comment != "keep 'quoted' comment" {
		t.Fatalf("comment changed: %q", comment)
	}
	execFixture(t, db, `CREATE ROLE enum_owner; ALTER TYPE public.status ADD VALUE 'b'; ALTER TYPE public.status OWNER TO enum_owner`)
	p = planEnum(t, db, "a")
	if _, err := db.ExecContext(t.Context(), p.Statements[0].SQL); err == nil {
		t.Fatal("rebuild changed type ownership")
	}
	var owner string
	if err := db.QueryRowContext(t.Context(), `SELECT typowner::regrole::text FROM pg_type WHERE oid='public.status'::regtype`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "enum_owner" {
		t.Fatalf("owner changed after refused rebuild: %q", owner)
	}
}

func TestEnumDefaultChanges(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `CREATE TYPE public.status AS ENUM ('a','b','c'); CREATE TABLE public.items (state public.status DEFAULT 'c'); INSERT INTO public.items VALUES ('a')`)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, defaultSQL := range []string{"'a'::status", ""} {
		current, err := postgres.New().Inspect(t.Context(), conn, "public")
		if err != nil {
			t.Fatal(err)
		}
		desired, err := schemigo.Normalize(current)
		if err != nil {
			t.Fatal(err)
		}
		desired.Enums[0].Values = desired.Enums[0].Values[:len(desired.Enums[0].Values)-1]
		desired.Tables[0].Columns[0].Default = defaultSQL
		p, err := postgres.New().Diff(t.Context(), conn, current, desired)
		if err != nil {
			t.Fatal(err)
		}
		executePlan(t, db, p)
		after, err := postgres.New().Inspect(t.Context(), conn, "public")
		if err != nil {
			t.Fatal(err)
		}
		p, err = postgres.New().Diff(t.Context(), conn, after, desired)
		if err != nil || len(p.Statements) != 0 {
			t.Fatalf("default change did not converge: %+v, %v", p, err)
		}
	}
	execFixture(t, db, `INSERT INTO public.items DEFAULT VALUES`)
	var nulls int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM public.items WHERE state IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Fatalf("dropped default still applied: null rows=%d", nulls)
	}
}
