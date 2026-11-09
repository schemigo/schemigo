package postgres

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/schemigo/schemigo"
)

func TestInspectRoutinesAndViews(t *testing.T) {
	c := &catalogConn{values: []driver.Value{
		true, "pg_catalog", nil, nil, nil, nil, nil,
		[]byte(`{"name":"users","columns":[{"name":"id","type":"bigint"}]}`),
		[]driver.Value{
			[]byte(`{"name":"get_user_orders","parameters":"p_user_id bigint","returns":"TABLE(order_id bigint, total numeric)","language":"sql","body":"SELECT id, total FROM orders WHERE user_id = p_user_id;"}`),
			[]byte(`{"name":"get_user_orders","parameters":"p_email text","returns":"bigint","language":"sql","body":"SELECT id FROM users WHERE email = p_email;"}`),
			[]byte(`{"name":"touch","parameters":"","returns":"trigger","language":"plpgsql","body":"BEGIN RETURN NEW; END"}`),
		},
		[]driver.Value{
			[]byte(`{"name":"row_trigger","table":"users","when":"BEFORE","event":"UPDATE","for_each":"ROW","definition":"CREATE TRIGGER row_trigger BEFORE UPDATE ON users FOR EACH ROW WHEN (old.name IS DISTINCT FROM new.name) EXECUTE FUNCTION touch()","function":"touch"}`),
			[]byte(`{"name":"statement_trigger","table":"users","when":"AFTER","event":"INSERT OR DELETE OR UPDATE OR TRUNCATE","for_each":"STATEMENT","function":"touch"}`),
			[]byte(`{"name":"view_trigger","table":"active_users","when":"INSTEAD OF","event":"UPDATE","for_each":"ROW","function":"touch"}`),
		},
		[]driver.Value{
			[]byte(`{"name":"active_users","definition":"SELECT id FROM users","materialized":false}`),
			[]byte(`{"name":"user_ids","definition":"SELECT id FROM users","materialized":true}`),
		},
	}}
	db := sql.OpenDB(catalogConnector{conn: c})
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, err := New().Inspect(t.Context(), conn, `a'".schema`)
	if err != nil {
		t.Fatal(err)
	}
	functions := []schemigo.Function{
		{Name: "get_user_orders", Parameters: "p_user_id bigint", Returns: "TABLE(order_id bigint, total numeric)", Language: "sql", Body: "SELECT id, total FROM orders WHERE user_id = p_user_id;"},
		{Name: "get_user_orders", Parameters: "p_email text", Returns: "bigint", Language: "sql", Body: "SELECT id FROM users WHERE email = p_email;"},
		{Name: "touch", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END"},
	}
	triggers := []schemigo.Trigger{
		{Name: "row_trigger", Table: "users", When: "BEFORE", Event: "UPDATE", ForEach: "ROW", ForEachRow: true, Condition: "old.name IS DISTINCT FROM new.name", Function: "touch"},
		{Name: "statement_trigger", Table: "users", When: "AFTER", Event: "INSERT OR DELETE OR UPDATE OR TRUNCATE", ForEach: "STATEMENT", Function: "touch"},
		{Name: "view_trigger", Table: "active_users", When: "INSTEAD OF", Event: "UPDATE", ForEach: "ROW", ForEachRow: true, Function: "touch"},
	}
	views := []schemigo.View{
		{Name: "active_users", Definition: "SELECT id FROM users"},
		{Name: "user_ids", Definition: "SELECT id FROM users", Materialized: true},
	}
	if !reflect.DeepEqual(got.Functions, functions) {
		t.Fatalf("functions = %+v", got.Functions)
	}
	if !reflect.DeepEqual(got.Triggers, triggers) {
		t.Fatalf("triggers = %+v", got.Triggers)
	}
	if !reflect.DeepEqual(got.Views, views) {
		t.Fatalf("views = %+v", got.Views)
	}
}

func TestTriggerCondition(t *testing.T) {
	tests := []struct{ name, definition, want string }{
		{name: "simple", definition: `CREATE TRIGGER t BEFORE UPDATE ON public.users FOR EACH ROW WHEN ((old.name IS DISTINCT FROM new.name)) EXECUTE FUNCTION touch()`, want: `(old.name IS DISTINCT FROM new.name)`},
		{name: "quoted identifiers", definition: `CREATE TRIGGER "prefix WHEN (false) EXECUTE FUNCTION bogus" AFTER UPDATE ON "schema(1)"."table)" FOR EACH ROW WHEN ((old.name IS DISTINCT FROM new.name)) EXECUTE FUNCTION "function) EXECUTE FUNCTION bogus"()`, want: `(old.name IS DISTINCT FROM new.name)`},
		{name: "quoted parentheses", definition: `CREATE TRIGGER "t""(" BEFORE UPDATE ON public.users FOR EACH ROW WHEN ((new.name = ') EXECUTE FUNCTION fake('::text) AND (new."x)" = 'it''s (fine)'::text)) EXECUTE FUNCTION touch()`, want: `(new.name = ') EXECUTE FUNCTION fake('::text) AND (new."x)" = 'it''s (fine)'::text)`},
		{name: "escape string", definition: `CREATE TRIGGER t BEFORE UPDATE ON public.users FOR EACH ROW WHEN ((new.name = E'it\'s (fine)'::text)) EXECUTE FUNCTION touch()`, want: `(new.name = E'it\'s (fine)'::text)`},
		{name: "standard string", definition: `CREATE TRIGGER t BEFORE UPDATE ON public.users FOR EACH ROW WHEN ((new.name = 'trailing\'::text)) EXECUTE FUNCTION touch()`, want: `(new.name = 'trailing\'::text)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := triggerCondition(tt.definition)
			if err != nil || got != tt.want {
				t.Fatalf("condition=%q, error=%v, want %q", got, err, tt.want)
			}
		})
	}
	for _, definition := range []string{"", `CREATE TRIGGER "unclosed(`, `WHEN ((x = 'unclosed)`, `WHEN ((x)`} {
		t.Run("invalid "+definition, func(t *testing.T) {
			if _, err := triggerCondition(definition); err == nil {
				t.Fatalf("accepted incomplete definition %q", definition)
			}
		})
	}
}
