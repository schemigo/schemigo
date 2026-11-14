//go:build integration

package mysql_test

import (
	"database/sql"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/internal/testdb"
	"github.com/schemigo/schemigo/mysql"
)

func TestMySQLDiffRoundTrip(t *testing.T) {
	db, _ := testdb.MySQLURL(t)
	name := mysqlSchema(t, db)
	driver := mysql.New()
	desired := schemigo.Schema{Name: name, Tables: []schemigo.Table{{
		Name: "order",
		Columns: []schemigo.Column{
			{Name: "id", Type: "bigint", NotNull: true, PrimaryKey: true},
			{Name: "status", Type: "varchar(32)", Default: "'pending'"},
		},
		Indexes: []schemigo.Index{{Name: "idx_status", Columns: []string{"status"}, Unique: true}},
		Checks:  []schemigo.Check{{Name: "chk_status", Expression: "status <> ''"}},
	}}}
	if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	assertMySQLNoDrift(t, db, driver, name, desired)

	desired.Tables[0].Columns[1].Name = "state"
	desired.Tables[0].Indexes[0].Columns[0] = "state"
	desired.Tables[0].Checks[0].Expression = "state <> ''"
	desired.Tables = append(desired.Tables, schemigo.Table{
		Name: "items",
		Columns: []schemigo.Column{
			{Name: "id", Type: "bigint", NotNull: true, PrimaryKey: true},
			{Name: "order_id", Type: "bigint"},
		},
		Indexes: []schemigo.Index{{Name: "idx_order", Columns: []string{"order_id"}}},
		ForeignKeys: []schemigo.ForeignKey{{Name: "fk_order", Columns: []string{"order_id"},
			ReferencedTable: "order", ReferencedColumns: []string{"id"}}},
	})
	if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	assertMySQLNoDrift(t, db, driver, name, desired)
}

func TestMySQLRoutineTriggerViewRoundTrip(t *testing.T) {
	db, _ := testdb.MySQLURL(t)
	name := mysqlSchema(t, db)
	driver := mysql.New()
	desired := schemigo.Schema{
		Name:      name,
		Tables:    []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint", NotNull: true, PrimaryKey: true}}}},
		Functions: []schemigo.Function{{Name: "audit_user", Body: "BEGIN SET @audit_seen = 1; END"}},
		Triggers:  []schemigo.Trigger{{Name: "users_audit", Table: "users", When: "AFTER", Event: "INSERT", Function: "audit_user"}},
		Views:     []schemigo.View{{Name: "user_ids", Definition: "SELECT id FROM users"}},
	}
	if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	assertMySQLNoDrift(t, db, driver, name, desired)
	desired.Views[0].Definition = "SELECT id AS uid FROM users"
	desired.Functions[0].Body = "BEGIN SET @audit_seen = 2; END"
	if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	assertMySQLNoDrift(t, db, driver, name, desired)
}

func TestMySQLFunctionRoundTrip(t *testing.T) {
	db, _ := testdb.MySQLURL(t)
	name := mysqlSchema(t, db)
	driver := mysql.New()
	desired := schemigo.Schema{Name: name, Functions: []schemigo.Function{{Name: "inc", Parameters: "x int", Returns: "int", Body: "DETERMINISTIC READS SQL DATA RETURN x + 1"}}}
	if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	assertMySQLNoDrift(t, db, driver, name, desired)
}

func assertMySQLNoDrift(t *testing.T, db *sql.DB, driver schemigo.Driver, name string, desired schemigo.Schema) {
	t.Helper()
	current, err := driver.Inspect(t.Context(), mustConn(t, db), name)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := driver.Diff(t.Context(), nil, current, desired)
	if err != nil || len(plan.Statements) != 0 {
		t.Fatalf("applied schema has drift: %s, %v\ncurrent: %+v", plan.SQL(), err, current)
	}
}
