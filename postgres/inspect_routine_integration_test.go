//go:build integration

package postgres_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/internal/testdb"
)

func TestInspectPostgresRoutinesAndViews(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `
 CREATE TABLE public.users (id bigint PRIMARY KEY, email text, name text, deleted_at timestamptz);
 CREATE TABLE public.orders (id bigint, user_id bigint, total numeric);
 CREATE FUNCTION public.get_user_orders(p_user_id int8)
 RETURNS TABLE(order_id int8, total numeric) LANGUAGE sql AS
 $$SELECT id, total FROM public.orders WHERE user_id = p_user_id;$$;
 CREATE FUNCTION public.get_user_orders(p_email text)
 RETURNS bigint LANGUAGE sql AS $$SELECT id FROM public.users WHERE email = p_email;$$;
 CREATE FUNCTION public.update_modified_column() RETURNS trigger LANGUAGE plpgsql AS
 $$BEGIN RETURN NEW; END$$;
 CREATE TRIGGER trg_update_timestamp BEFORE UPDATE ON public.users
 FOR EACH ROW WHEN (OLD.name IS DISTINCT FROM NEW.name)
 EXECUTE FUNCTION public.update_modified_column();
 CREATE TRIGGER statement_trigger AFTER TRUNCATE OR UPDATE OR DELETE OR INSERT ON public.users
 FOR EACH STATEMENT EXECUTE FUNCTION public.update_modified_column();
 CREATE TRIGGER "name WHEN (false) EXECUTE FUNCTION bogus" AFTER UPDATE ON public.users
 FOR EACH ROW EXECUTE FUNCTION public.update_modified_column();
 CREATE TRIGGER "prefix WHEN (false) EXECUTE FUNCTION bogus" AFTER UPDATE ON public.users
 FOR EACH ROW WHEN (OLD.name IS DISTINCT FROM NEW.name)
 EXECUTE FUNCTION public.update_modified_column();
 CREATE CONSTRAINT TRIGGER constraint_trigger AFTER UPDATE ON public.users
 DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION public.update_modified_column();
 CREATE VIEW public.active_users AS SELECT id, email, name FROM public.users WHERE deleted_at IS NULL;
 CREATE TRIGGER view_trigger INSTEAD OF UPDATE ON public.active_users
 FOR EACH ROW EXECUTE FUNCTION public.update_modified_column();
 CREATE MATERIALIZED VIEW public.user_ids AS SELECT id FROM public.users;
 CREATE TABLE public.schema_migrations (hash text);
 CREATE TRIGGER history_trigger BEFORE UPDATE ON public.schema_migrations
 FOR EACH ROW EXECUTE FUNCTION public.update_modified_column();
 `)
	s := inspect(t, db, "public")
	if len(s.Functions) != 3 || len(s.Triggers) != 5 || len(s.Views) != 2 {
		t.Fatalf("unexpected routines/views: %+v", s)
	}
	f := s.Functions[0]
	if f.Parameters == "p_email text" {
		f = s.Functions[1]
	}
	if f.Name != "get_user_orders" || f.Parameters != "p_user_id bigint" || f.Returns != "TABLE(order_id bigint, total numeric)" || f.Language != "sql" || f.Body != "SELECT id, total FROM public.orders WHERE user_id = p_user_id;" {
		t.Fatalf("function=%+v", f)
	}
	if s.Functions[0].Parameters != "p_email text" && s.Functions[1].Parameters != "p_email text" {
		t.Fatalf("overload lost: %+v", s.Functions)
	}
	triggers := map[string]schemigo.Trigger{}
	for _, tr := range s.Triggers {
		triggers[tr.Name] = tr
	}
	tr := triggers["trg_update_timestamp"]
	if tr.Name != "trg_update_timestamp" || tr.When != "BEFORE" || tr.Event != "UPDATE" || tr.Table != "users" || tr.ForEach != "ROW" || !tr.ForEachRow || tr.Function != "update_modified_column" || !strings.Contains(tr.Condition, "old.name IS DISTINCT FROM new.name") {
		t.Fatalf("trigger=%+v", tr)
	}
	statement := triggers["statement_trigger"]
	if statement.ForEach != "STATEMENT" || statement.ForEachRow || statement.Event != "INSERT OR DELETE OR UPDATE OR TRUNCATE" || statement.Condition != "" {
		t.Fatalf("statement trigger=%+v", statement)
	}
	viewTrigger := triggers["view_trigger"]
	if viewTrigger.Table != "active_users" || viewTrigger.When != "INSTEAD OF" || viewTrigger.ForEach != "ROW" || !viewTrigger.ForEachRow {
		t.Fatalf("view trigger=%+v", viewTrigger)
	}
	quoted, ok := triggers["name WHEN (false) EXECUTE FUNCTION bogus"]
	if !ok || quoted.Condition != "" {
		t.Fatalf("quoted trigger name became a WHEN condition: %+v", quoted)
	}
	if conditional := triggers["prefix WHEN (false) EXECUTE FUNCTION bogus"]; conditional.Condition != tr.Condition {
		t.Fatalf("trigger name changed WHEN: %+v, want %q", conditional, tr.Condition)
	}
	if s.Views[0].Name != "active_users" || s.Views[0].Materialized || !strings.Contains(s.Views[0].Definition, "deleted_at IS NULL") || strings.HasSuffix(strings.TrimSpace(s.Views[0].Definition), ";") || !s.Views[1].Materialized {
		t.Fatalf("views=%+v", s.Views)
	}
	again := inspect(t, db, "public")
	if !reflect.DeepEqual(s, again) {
		t.Fatal("routine/view inspection is not deterministic")
	}
}

func TestInspectPostgresRoutineIsolation(t *testing.T) {
	db := testdb.Postgres(t)
	execFixture(t, db, `
 CREATE SCHEMA other;
 CREATE FUNCTION public.echo(p_value text DEFAULT 'public') RETURNS text LANGUAGE sql AS $$SELECT p_value;$$;
 CREATE FUNCTION other.echo(p_value text DEFAULT 'other') RETURNS text LANGUAGE sql AS $$SELECT upper(p_value);$$;
 CREATE VIEW public.literal AS SELECT ';'::text AS value;
 CREATE VIEW other.literal AS SELECT 'other'::text AS value;
 CREATE VIEW public.schema_migrations AS SELECT 'hidden'::text AS hash;
 `)
	public := inspect(t, db, "public")
	other := inspect(t, db, "other")
	if len(public.Functions) != 1 || len(other.Functions) != 1 || len(public.Views) != 1 || len(other.Views) != 1 {
		t.Fatalf("unexpected objects: public=%+v, other=%+v", public, other)
	}
	if public.Functions[0].Name != "echo" || public.Functions[0].Body != "SELECT p_value;" ||
		!strings.Contains(public.Functions[0].Parameters, "DEFAULT 'public'") || other.Functions[0].Body != "SELECT upper(p_value);" {
		t.Fatalf("functions from different namespaces mixed: public=%+v, other=%+v", public.Functions, other.Functions)
	}
	if !strings.Contains(public.Views[0].Definition, "';'") || strings.Contains(public.Views[0].Definition, "other") ||
		!strings.Contains(other.Views[0].Definition, "other") {
		t.Fatalf("view literal or namespace lost: public=%+v, other=%+v", public.Views, other.Views)
	}
	for _, s := range []schemigo.Schema{public, other} {
		if strings.HasSuffix(strings.TrimSpace(s.Views[0].Definition), ";") {
			t.Fatalf("view has statement terminator: %q", s.Views[0].Definition)
		}
	}
}
