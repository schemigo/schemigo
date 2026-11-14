//go:build integration

package main

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schemigo/schemigo/internal/testdb"
)

func TestCLIMySQLWorkflow(t *testing.T) {
	_, dsn := testdb.MySQLURL(t)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(".", "_schemigo_mysql_cli_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	dir := filepath.Join(root, "schema")
	var out, diagnostics bytes.Buffer
	if code := run(t.Context(), []string{"table", "-dir", dir, "-schema", strings.TrimPrefix(u.Path, "/"), "users", "id:bigint"}, strings.NewReader(""), &out, &diagnostics); code != 0 {
		t.Fatalf("scaffold code=%d stderr=%q", code, diagnostics.String())
	}
	call := func(flags ...string) (int, string, string) {
		t.Helper()
		out.Reset()
		diagnostics.Reset()
		args := append([]string{"-d", dsn, "-dir", dir}, flags...)
		code := run(t.Context(), args, strings.NewReader(""), &out, &diagnostics)
		return code, out.String(), diagnostics.String()
	}
	if code, output, diagnostic := call("-dry-run"); code != 0 || !strings.Contains(output, "CREATE TABLE") {
		t.Fatalf("dry-run code=%d out=%q stderr=%q", code, output, diagnostic)
	}
	if code, output, diagnostic := call("-yes"); code != 0 || !strings.Contains(output, "Applied.") {
		t.Fatalf("apply code=%d out=%q stderr=%q", code, output, diagnostic)
	}
	if code, output, diagnostic := call("-history"); code != 0 || strings.TrimSpace(output) == "" {
		t.Fatalf("history code=%d out=%q stderr=%q", code, output, diagnostic)
	}
	if code, output, diagnostic := call("-yes"); code != 0 || !strings.Contains(output, "No changes.") {
		t.Fatalf("second apply code=%d out=%q stderr=%q", code, output, diagnostic)
	}
}
