//go:build integration

package mysql_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/internal/testdb"
	"github.com/schemigo/schemigo/mysql"
)

func mysqlSchema(t *testing.T, db *sql.DB) string {
	t.Helper()
	var name string
	if err := db.QueryRowContext(t.Context(), "SELECT DATABASE()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func mysqlDesired(name string) schemigo.Schema {
	return schemigo.Schema{Name: name, Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint", NotNull: true, PrimaryKey: true}}}}}
}

func TestMySQLHistorySkip(t *testing.T) {
	db, _ := testdb.MySQLURL(t)
	name, driver := mysqlSchema(t, db), mysql.New()
	if err := schemigo.Apply(t.Context(), db, driver, name, schemigo.Schema{Name: name}, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	history, err := schemigo.History(t.Context(), db, driver, name)
	if err != nil || len(history) != 0 {
		t.Fatalf("empty plan created history: %+v %v", history, err)
	}
	desired := mysqlDesired(name)
	for i := 0; i < 2; i++ {
		if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
			t.Fatal(err)
		}
	}
	history, err = schemigo.History(t.Context(), db, driver, name)
	if err != nil || len(history) != 1 {
		t.Fatalf("no-op was recorded: %+v %v", history, err)
	}
}

func TestMySQLRepeatedHashAfterDrift(t *testing.T) {
	db, _ := testdb.MySQLURL(t)
	name, driver := mysqlSchema(t, db), mysql.New()
	desired := mysqlDesired(name)
	if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "DROP TABLE `users`"); err != nil {
		t.Fatal(err)
	}
	if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	history, err := schemigo.History(t.Context(), db, driver, name)
	if err != nil || len(history) != 2 || history[0].Hash != history[1].Hash {
		t.Fatalf("drift was not repaired and recorded: %+v %v", history, err)
	}
}

func TestMySQLRetryAfterPartialDDL(t *testing.T) {
	db, _ := testdb.MySQLURL(t)
	name, driver := mysqlSchema(t, db), mysql.New()
	desired := mysqlDesired(name)
	desired.Tables = append(desired.Tables, schemigo.Table{Name: "orders", Columns: []schemigo.Column{{Name: "id", Type: "not_a_mysql_type"}}})
	backupRoot, err := os.MkdirTemp(".", "_mysql_backup_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(backupRoot) })
	err = schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{BackupDir: filepath.Join(backupRoot, "snapshots")})
	var failure *schemigo.ApplyError
	if !errors.As(err, &failure) || failure.BackupPath == "" || !strings.Contains(err.Error(), "statement 2") {
		t.Fatalf("partial failure lacks completed-step or snapshot detail: %v", err)
	}
	if _, err := os.Stat(failure.BackupPath); err != nil {
		t.Fatalf("snapshot missing after failure: %v", err)
	}
	desired.Tables[1].Columns[0].Type = "bigint"
	if err := schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatalf("retry after partial DDL: %v", err)
	}
	current, err := driver.Inspect(t.Context(), mustConn(t, db), name)
	if err != nil || len(current.Tables) != 2 {
		t.Fatalf("retry did not finish: %+v %v", current, err)
	}
}

func TestMySQLConcurrentApply(t *testing.T) {
	db, _ := testdb.MySQLURL(t)
	name, driver := mysqlSchema(t, db), mysql.New()
	desired := mysqlDesired(name)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			results <- schemigo.Apply(t.Context(), db, driver, name, desired, schemigo.ApplyOptions{NoBackup: true})
		})
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := schemigo.History(t.Context(), db, driver, name)
	if err != nil || len(history) != 1 {
		t.Fatalf("concurrent apply produced %+v: %v", history, err)
	}
}

func mustConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
