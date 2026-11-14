//go:build integration

package main

import (
	"bytes"
	"github.com/schemigo/schemigo/internal/testdb"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClickHouseCLI(t *testing.T) {
	db, dsn := testdb.ClickHouseURL(t)
	endpoint := testdb.Keeper(t)
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE default.events (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(".", "_clickhouse_cli_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	dir := filepath.Join(root, "schema")
	invoke := func(extra ...string) string {
		t.Helper()
		var out, diagnostics bytes.Buffer
		args := append([]string{"-d", dsn, "-dir", dir}, extra...)
		if code := run(t.Context(), args, strings.NewReader("Y\n"), &out, &diagnostics); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", extra, code, diagnostics.String())
		}
		return out.String()
	}
	invoke("-export")
	if _, err := db.ExecContext(t.Context(), "DROP TABLE default.events"); err != nil {
		t.Fatal(err)
	}
	if got := invoke("-dry-run"); !strings.Contains(got, "CREATE TABLE") {
		t.Fatalf("preview=%q", got)
	}
	var count uint64
	if err := db.QueryRowContext(t.Context(), "SELECT count() FROM system.tables WHERE database = 'default'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("dry-run wrote DDL: %d %v", count, err)
	}
	invoke("-keeper", endpoint, "-yes", "-no-backup")
	if got := invoke("-keeper", endpoint, "-yes"); !strings.Contains(got, "No changes") {
		t.Fatalf("repeat=%q", got)
	}
	if got := invoke("-history"); got == "" {
		t.Fatal("history is empty")
	}
}
