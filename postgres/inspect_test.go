package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// A scripted connection checks read-only transaction use and error handling;
// PostgreSQL query semantics are covered separately by integration tests.
type catalogConnector struct{ conn *catalogConn }

func (c catalogConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (catalogConnector) Driver() driver.Driver                          { return catalogDriver{} }

type catalogDriver struct{}

func (catalogDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type catalogConn struct {
	queries               []string
	values                []driver.Value
	failAt                int
	cause                 error
	options               driver.TxOptions
	committed, rolledBack bool
}

func (*catalogConn) Prepare(string) (driver.Stmt, error) { return nil, errors.ErrUnsupported }
func (*catalogConn) Close() error                        { return nil }
func (*catalogConn) Begin() (driver.Tx, error)           { return nil, errors.ErrUnsupported }
func (c *catalogConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.options = options
	return c, nil
}
func (c *catalogConn) Commit() error   { c.committed = true; return nil }
func (c *catalogConn) Rollback() error { c.rolledBack = true; return nil }
func (c *catalogConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries = append(c.queries, q)
	if len(args) != 1 || args[0].Value != `a'".schema` {
		return nil, errors.New("schema must be bound as one parameter")
	}
	if c.failAt == len(c.queries) {
		return nil, c.cause
	}
	var value driver.Value
	if len(c.values) >= len(c.queries) {
		value = c.values[len(c.queries)-1]
	}
	return &catalogRows{value: value}, nil
}

type catalogRows struct {
	value driver.Value
	done  bool
}

func (*catalogRows) Columns() []string { return []string{"catalog"} }
func (*catalogRows) Close() error      { return nil }
func (r *catalogRows) Next(dest []driver.Value) error {
	if r.done || r.value == nil {
		return io.EOF
	}
	dest[0] = r.value
	r.done = true
	return nil
}

func TestInspectSnapshot(t *testing.T) {
	c := &catalogConn{values: []driver.Value{true, "pg_catalog", nil, nil, nil,
		[]byte(`{"name":"pg_trgm","version":"1.6"}`),
		[]byte(`{"name":"status","values":["open","closed"]}`),
		[]byte(`{"name":"users","columns":[{"name":"id","type":"bigint","not_null":true}],"primary_key":["id"]}`),
		[]byte(`{"name":"touch","returns":"trigger","language":"plpgsql","body":"BEGIN RETURN NEW; END"}`),
		[]byte(`{"name":"touch","table":"users","when":"BEFORE","event":"UPDATE","function":"touch","for_each":"ROW"}`),
		[]byte(`{"name":"active","definition":"SELECT id FROM users","materialized":false}`),
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
	if got.Name != `a'".schema` || len(got.Tables) != 1 || got.Tables[0].Name != "users" || len(got.Enums) != 1 || len(got.Extensions) != 1 || len(got.Views) != 1 || !got.Triggers[0].ForEachRow {
		t.Fatalf("incomplete schema: %+v", got)
	}
	if !c.options.ReadOnly || c.options.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || !c.committed || c.rolledBack {
		t.Fatalf("unexpected snapshot: %+v", c)
	}
}

func TestInspectFailure(t *testing.T) {
	cause := errors.New("catalog unavailable")
	for _, stage := range []struct {
		name string
		at   int
	}{{name: "schema", at: 1}, {name: "search path", at: 2}, {name: "capabilities", at: 3}, {name: "tables", at: 8}, {name: "views", at: 11}} {
		t.Run(stage.name, func(t *testing.T) {
			c := &catalogConn{values: []driver.Value{true, "pg_catalog"}, failAt: stage.at, cause: cause}
			db := sql.OpenDB(catalogConnector{conn: c})
			defer db.Close()
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			got, err := New().Inspect(t.Context(), conn, `a'".schema`)
			if !errors.Is(err, cause) || !reflect.ValueOf(got).IsZero() || !c.rolledBack || c.committed {
				t.Fatalf("schema=%+v, err=%v, connection=%+v", got, err, c)
			}
		})
	}
}

func TestInspectRejectsInvalidInput(t *testing.T) {
	if _, err := New().Inspect(t.Context(), nil, "public"); err == nil {
		t.Fatal("nil connection accepted")
	}
	c := &catalogConn{}
	db := sql.OpenDB(catalogConnector{conn: c})
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, name := range []string{"", "bad\x00name"} {
		if _, err := New().Inspect(t.Context(), conn, name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if len(c.queries) != 0 {
		t.Fatal("invalid input queried database")
	}
	c.values = []driver.Value{false}
	_, err = New().Inspect(t.Context(), conn, `a'".schema`)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing namespace: %v", err)
	}
}

func TestInspectInvalidCatalog(t *testing.T) {
	c := &catalogConn{values: []driver.Value{true, "pg_catalog", nil, nil, nil, []byte(`invalid JSON`)}}
	db := sql.OpenDB(catalogConnector{conn: c})
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, err := New().Inspect(t.Context(), conn, `a'".schema`)
	if err == nil || !strings.Contains(err.Error(), "decode catalog") || !reflect.ValueOf(got).IsZero() || !c.rolledBack {
		t.Fatalf("schema=%+v, error=%v, rollback=%v", got, err, c.rolledBack)
	}
}
