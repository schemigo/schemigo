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

func execFixture(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query); err != nil {
		t.Fatalf("fixture: %v", err)
	}
}

func inspect(t *testing.T, db *sql.DB, name string) schemigo.Schema {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s, err := postgres.New().Inspect(t.Context(), conn, name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func table(t *testing.T, s schemigo.Schema, name string) schemigo.Table {
	t.Helper()
	for _, table := range s.Tables {
		if table.Name == name {
			return table
		}
	}
	t.Fatalf("missing table %q in %+v", name, s.Tables)
	return schemigo.Table{}
}

func TestInspectPostgresCore(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `
 CREATE EXTENSION pg_trgm;
 CREATE TYPE public.order_status AS ENUM ('pending', 'processing', 'shipped');
 CREATE TABLE public.schema_migrations (hash text PRIMARY KEY);
 CREATE TABLE public.users (
  id bigserial PRIMARY KEY, email varchar(255) NOT NULL, name text,
  created_at timestamptz DEFAULT now(),
  CONSTRAINT chk_users_email CHECK (email ~ '^[^@]+@[^@]+$')
 );
 CREATE UNIQUE INDEX idx_users_email ON public.users (email);
 CREATE INDEX idx_users_lower_email ON public.users (lower(email));
 CREATE INDEX idx_users_gin ON public.users USING gin (email gin_trgm_ops);
 CREATE TABLE public.orders (
  id bigint NOT NULL, user_id bigint NOT NULL, total numeric(10,2),
  status public.order_status DEFAULT 'pending',
  PRIMARY KEY (user_id, id),
  CONSTRAINT fk_orders_user_id FOREIGN KEY (user_id) REFERENCES public.users (id) ON DELETE CASCADE,
  CONSTRAINT chk_orders_total CHECK (total >= 0)
 );
 CREATE INDEX idx_orders_active ON public.orders (user_id) WHERE status = 'pending';
 CREATE SCHEMA other;
 CREATE TABLE other.users (other_id text);
 `)
	s := inspect(t, db, "public")
	if s.Name != "public" || len(s.Tables) != 2 || len(s.Enums) != 1 || len(s.Extensions) != 1 {
		t.Fatalf("unexpected objects: %+v", s)
	}
	if s.Enums[0].Name != "order_status" || !reflect.DeepEqual(s.Enums[0].Values, []string{"pending", "processing", "shipped"}) {
		t.Fatalf("enum = %+v", s.Enums)
	}
	foundExtension := false
	for _, e := range s.Extensions {
		if e.Name == "pg_trgm" && e.Version != "" {
			foundExtension = true
		}
	}
	if !foundExtension {
		t.Fatalf("extension version missing: %+v", s.Extensions)
	}
	users := table(t, s, "users")
	if !reflect.DeepEqual(users.PrimaryKey, []string{"id"}) || users.Columns[0].PrimaryKey || users.Columns[0].Type != postgres.Int8 || !strings.Contains(users.Columns[0].Default, "users_id_seq") || !users.Columns[0].NotNull {
		t.Fatalf("id = %+v, PK=%v", users.Columns[0], users.PrimaryKey)
	}
	if users.Columns[1].Type != postgres.VarChar255 || !users.Columns[1].NotNull || users.Columns[3].Type != postgres.TimestampTZ || users.Columns[3].Default != "now()" {
		t.Fatalf("columns = %+v", users.Columns)
	}
	if len(users.Indexes) != 3 || users.Indexes[0].Name != "idx_users_email" || !users.Indexes[0].Unique || !reflect.DeepEqual(users.Indexes[0].Columns, []string{"email"}) || users.Indexes[1].Method != "gin" || !strings.Contains(users.Indexes[1].Expression, "gin_trgm_ops") || users.Indexes[2].Expression != "lower((email)::text)" {
		t.Fatalf("indexes = %+v", users.Indexes)
	}
	if len(users.Checks) != 1 || users.Checks[0].Name != "chk_users_email" || !strings.Contains(users.Checks[0].Expression, "email") {
		t.Fatalf("checks = %+v", users.Checks)
	}
	orders := table(t, s, "orders")
	if !reflect.DeepEqual(orders.PrimaryKey, []string{"user_id", "id"}) || orders.Columns[2].Type != postgres.Numeric102 || !strings.Contains(orders.Columns[3].Type, "order_status") {
		t.Fatalf("orders = %+v", orders)
	}
	wantFK := schemigo.ForeignKey{Name: "fk_orders_user_id", Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "NO ACTION"}
	if !reflect.DeepEqual(orders.ForeignKeys, []schemigo.ForeignKey{wantFK}) || len(orders.Checks) != 1 || !strings.Contains(orders.Indexes[0].Where, "pending") {
		t.Fatalf("constraints/indexes = %+v", orders)
	}
	if again := inspect(t, db, "public"); !reflect.DeepEqual(s, again) {
		t.Fatal("inspection is not deterministic")
	}
	if other := inspect(t, db, "other"); len(other.Tables) != 1 || other.Tables[0].Columns[0].Name != "other_id" {
		t.Fatalf("namespaces mixed: %+v", other)
	}
}

func TestInspectPostgresRejectsUnsupported(t *testing.T) {
	db := testdb.Postgres(t)
	tests := []struct{ name, ddl, want string }{
		{name: "identity", ddl: `CREATE TABLE x.t (id bigint GENERATED ALWAYS AS IDENTITY)`, want: "identity"},
		{name: "include", ddl: `CREATE TABLE x.t (id int, value text); CREATE INDEX inc ON x.t (id) INCLUDE (value)`, want: "INCLUDE"},
		{name: "cross schema FK", ddl: `CREATE TABLE x.t (id int REFERENCES public.target(id))`, want: "constraint"},
		{name: "function options", ddl: `CREATE FUNCTION x.f() RETURNS int LANGUAGE sql IMMUTABLE AS 'SELECT 1'`, want: "function options"},
	}
	execFixture(t, db, `CREATE TABLE public.target (id int PRIMARY KEY)`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			execFixture(t, db, `CREATE SCHEMA x`)
			defer execFixture(t, db, `DROP SCHEMA x CASCADE`)
			execFixture(t, db, tt.ddl)
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			got, err := postgres.New().Inspect(t.Context(), conn, "x")
			if err == nil || !strings.Contains(err.Error(), tt.want) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("schema=%+v, error=%v", got, err)
			}
		})
	}
}
