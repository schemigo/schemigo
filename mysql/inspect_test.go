package mysql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/schemigo/schemigo/mysql"
)

type catalogConnector struct{}

func (catalogConnector) Connect(context.Context) (driver.Conn, error) { return catalogConn{}, nil }
func (catalogConnector) Driver() driver.Driver                        { return catalogDriver{} }

type catalogDriver struct{}

func (catalogDriver) Open(string) (driver.Conn, error) { return catalogConn{}, nil }

type catalogConn struct{}

func (catalogConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (catalogConn) Close() error                        { return nil }
func (catalogConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected Begin") }

func (catalogConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "information_schema.tables"):
		return &catalogRows{columns: []string{"table_name", "table_type", "engine"}, values: [][]driver.Value{{"order", "BASE TABLE", "InnoDB"}}}, nil
	case strings.Contains(query, "information_schema.columns"):
		return &catalogRows{columns: []string{"column_name", "column_type", "is_nullable", "column_default", "extra", "generation_expression"}, values: [][]driver.Value{{"id", "bigint", "NO", nil, "", nil}, {"status", "varchar(32)", "YES", "pending", "", nil}}}, nil
	case strings.Contains(query, "information_schema.key_column_usage k"):
		return &catalogRows{columns: []string{"constraint_name", "column_name", "referenced_table_name", "referenced_column_name", "delete_rule", "update_rule"}}, nil
	case strings.Contains(query, "information_schema.key_column_usage"):
		return &catalogRows{columns: []string{"column_name"}, values: [][]driver.Value{{"id"}}}, nil
	case strings.Contains(query, "information_schema.statistics"):
		return &catalogRows{columns: []string{"index_name", "non_unique", "column_name", "expression", "index_type"}, values: [][]driver.Value{{"idx_status", int64(0), "status", nil, "BTREE"}}}, nil
	case strings.Contains(query, "information_schema.table_constraints"):
		return &catalogRows{columns: []string{"constraint_name", "check_clause"}, values: [][]driver.Value{{"chk_status", "`status` <> ''"}}}, nil
	case strings.Contains(query, "information_schema.routines"):
		return &catalogRows{columns: []string{"routine_name", "specific_name", "routine_type", "dtd_identifier", "routine_definition"}}, nil
	case strings.Contains(query, "information_schema.triggers"):
		return &catalogRows{columns: []string{"trigger_name", "event_object_table", "action_timing", "event_manipulation", "action_statement", "action_orientation"}}, nil
	default:
		return nil, errors.New("unexpected catalog query: " + query)
	}
}

type catalogRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r catalogRows) Columns() []string { return r.columns }
func (r catalogRows) Close() error      { return nil }
func (r *catalogRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func TestMySQLInspectMapsCatalog(t *testing.T) {
	db := sql.OpenDB(catalogConnector{})
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	schema, err := mysql.New().Inspect(t.Context(), conn, "app")
	if err != nil {
		t.Fatal(err)
	}
	if schema.Name != "app" || len(schema.Tables) != 1 || schema.Tables[0].Name != "order" {
		t.Fatalf("tables: %+v", schema)
	}
	table := schema.Tables[0]
	if len(table.Columns) != 2 || !table.Columns[0].PrimaryKey || table.Columns[1].Default != "'pending'" || len(table.Indexes) != 1 || !table.Indexes[0].Unique || len(table.Checks) != 1 {
		t.Fatalf("catalog details lost: %+v", table)
	}
}
