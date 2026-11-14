//go:build integration

package mysql_test

import (
	"os"
	"testing"

	"github.com/schemigo/schemigo/internal/testdb"
	"github.com/schemigo/schemigo/mysql"
)

func TestMain(m *testing.M) { os.Exit(testdb.Run(m)) }

func TestMySQLInspectCore(t *testing.T) {
	db, _ := testdb.MySQLURL(t)
	_, err := db.ExecContext(t.Context(), "CREATE TABLE `order` (`id` BIGINT NOT NULL, `status` VARCHAR(32) DEFAULT 'pending', PRIMARY KEY (`id`), UNIQUE KEY `idx_status` (`status`), CONSTRAINT `chk_status` CHECK (`status` <> '')) ENGINE=InnoDB")
	if err != nil {
		t.Fatal(err)
	}
	var schemaName string
	if err := db.QueryRowContext(t.Context(), "SELECT DATABASE()").Scan(&schemaName); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	schema, err := mysql.New().Inspect(t.Context(), conn, schemaName)
	if err != nil {
		t.Fatal(err)
	}
	if schema.Name != schemaName || len(schema.Tables) != 1 || schema.Tables[0].Name != "order" {
		t.Fatalf("unexpected tables: %+v", schema)
	}
	table := schema.Tables[0]
	if len(table.Columns) != 2 || table.Columns[0].Name != "id" || table.Columns[0].Type != "bigint" || !table.Columns[0].PrimaryKey || table.Columns[1].Default != "'pending'" {
		t.Fatalf("unexpected columns: %+v", table.Columns)
	}
	if len(table.Indexes) != 1 || table.Indexes[0].Name != "idx_status" || !table.Indexes[0].Unique || len(table.Checks) != 1 || table.Checks[0].Name != "chk_status" {
		t.Fatalf("unexpected constraints: %+v", table)
	}
}
