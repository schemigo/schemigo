//go:build integration

package postgres_test

import (
	"reflect"
	"strings"
	"testing"

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
 CREATE TRIGGER statement_trigger AFTER INSERT OR UPDATE ON public.users
 FOR EACH STATEMENT EXECUTE FUNCTION public.update_modified_column();
 CREATE CONSTRAINT TRIGGER constraint_trigger AFTER UPDATE ON public.users
 DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION public.update_modified_column();
 CREATE VIEW public.active_users AS SELECT id, email, name FROM public.users WHERE deleted_at IS NULL;
 CREATE MATERIALIZED VIEW public.user_ids AS SELECT id FROM public.users;
 `)
	s := inspect(t, db, "public")
	if len(s.Functions) != 3 || len(s.Triggers) != 2 || len(s.Views) != 2 {
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
	tr := s.Triggers[1]
	if tr.Name != "trg_update_timestamp" || tr.When != "BEFORE" || tr.Event != "UPDATE" || tr.Table != "users" || tr.ForEach != "ROW" || !tr.ForEachRow || tr.Function != "update_modified_column" || !strings.Contains(tr.Condition, "old.name IS DISTINCT FROM new.name") {
		t.Fatalf("trigger=%+v", tr)
	}
	if s.Triggers[0].ForEach != "STATEMENT" || s.Triggers[0].ForEachRow || s.Triggers[0].Event != "INSERT OR UPDATE" || s.Triggers[0].Condition != "" {
		t.Fatalf("statement trigger=%+v", s.Triggers[0])
	}
	if s.Views[0].Name != "active_users" || s.Views[0].Materialized || !strings.Contains(s.Views[0].Definition, "deleted_at IS NULL") || strings.HasSuffix(strings.TrimSpace(s.Views[0].Definition), ";") || !s.Views[1].Materialized {
		t.Fatalf("views=%+v", s.Views)
	}
	again := inspect(t, db, "public")
	if !reflect.DeepEqual(s, again) {
		t.Fatal("routine/view inspection is not deterministic")
	}
}
