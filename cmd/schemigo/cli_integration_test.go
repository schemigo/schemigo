//go:build integration

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schemigo/schemigo/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Run(m)) }

func TestCLIPostgresWorkflow(t *testing.T) {
	db, dsn := testdb.PostgresURL(t)
	root, err := os.MkdirTemp(".", "_schemigo_cli_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	dir := filepath.Join(root, "schema")
	call := func(input string, flags ...string) (int, string, string) {
		t.Helper()
		var out, diagnostics bytes.Buffer
		args := append([]string{"-d", dsn, "-dir", dir}, flags...)
		code := run(t.Context(), args, strings.NewReader(input), &out, &diagnostics)
		return code, out.String(), diagnostics.String()
	}
	if code, _, diagnostic := call("", "-export"); code != 0 {
		t.Fatal(code, diagnostic)
	}
	entityPath := filepath.Join(dir, "tables", "users_schema.go")
	writeUser := func(withEmail bool) {
		t.Helper()
		columns := `[]Column{{Name:"id", Type:"bigint"}}`
		if withEmail {
			columns = `[]Column{{Name:"id", Type:"bigint"}, {Name:"email", Type:"text"}}`
		}
		if err := os.WriteFile(entityPath, []byte("package tables\nvar Users = Table{Name: \"users\", Columns: "+columns+"}\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeUser(false)
	allPath := filepath.Join(dir, "tables", "all.go")
	all, err := os.ReadFile(allPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allPath, []byte(strings.Replace(string(all), "[]Table{}", "[]Table{Users}", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	if code, out, diagnostic := call("", "-dry-run"); code != 0 || !strings.Contains(out, "CREATE TABLE") || strings.Contains(out, "[Y/n]") {
		t.Fatalf("dry-run code=%d out=%q err=%q", code, out, diagnostic)
	}
	var outputError bytes.Buffer
	if code := run(t.Context(), []string{"-d", dsn, "-dir", dir, "-dry-run"}, strings.NewReader(""), failingWriter{}, &outputError); code != 1 || !strings.Contains(outputError.String(), "output closed") {
		t.Fatalf("failed output code=%d err=%q", code, outputError.String())
	}
	var historyTable bool
	if err := db.QueryRowContext(t.Context(), `SELECT pg_catalog.to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&historyTable); err != nil || historyTable {
		t.Fatalf("dry-run changed history=%v err=%v", historyTable, err)
	}
	if code, out, diagnostic := call("n\n"); code != 0 || !strings.Contains(out, "[Y/n]") || diagnostic != "" {
		t.Fatalf("decline code=%d out=%q err=%q", code, out, diagnostic)
	}
	if code, out, diagnostic := call("Y\n"); code != 0 || !strings.Contains(out, "Applied.") || diagnostic != "" {
		t.Fatalf("apply code=%d out=%q err=%q", code, out, diagnostic)
	}
	if code, out, diagnostic := call("", "-history"); code != 0 || !strings.Contains(out, "schemigo") || diagnostic != "" {
		t.Fatalf("history code=%d out=%q err=%q", code, out, diagnostic)
	}
	if code, out, diagnostic := call(""); code != 0 || !strings.Contains(out, "No changes.") || diagnostic != "" {
		t.Fatalf("no-op code=%d out=%q err=%q", code, out, diagnostic)
	}
	writeUser(true)
	if code, out, diagnostic := call("", "-yes", "-no-backup"); code != 0 || !strings.Contains(out, "Applied.") || strings.Contains(out, "[Y/n]") || diagnostic != "" {
		t.Fatalf("yes code=%d out=%q err=%q", code, out, diagnostic)
	}
	var emailPresent bool
	if err := db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name='email')`).Scan(&emailPresent); err != nil || !emailPresent {
		t.Fatalf("-yes did not add email: present=%v err=%v", emailPresent, err)
	}
}
