package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
)

func scaffoldDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cwd, "_scaffold_test_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return filepath.Join(root, "schema")
}

func scaffoldRun(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	code := run(t.Context(), args, strings.NewReader(""), &out, &diagnostics)
	return code, diagnostics.String()
}

func TestScaffoldTable(t *testing.T) {
	dir := scaffoldDir(t)
	if code, diagnostics := scaffoldRun(t, "table", "-dir", dir, "users", "id:int8,email:varchar(255),total:numeric(10,2)"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []schemigo.Column{{Name: "id", Type: "int8"}, {Name: "email", Type: "varchar(255)"}, {Name: "total", Type: "numeric(10,2)"}}
	if got.Name != "public" || len(got.Tables) != 1 || got.Tables[0].Name != "users" || !reflect.DeepEqual(got.Tables[0].Columns, want) {
		t.Fatalf("unexpected schema: %+v", got)
	}
}

func TestScaffoldEnum(t *testing.T) {
	dir := scaffoldDir(t)
	if code, diagnostics := scaffoldRun(t, "enum", "-dir", dir, "order_status", "pending,processing,shipped,delivered,cancelled"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pending", "processing", "shipped", "delivered", "cancelled"}
	if len(got.Enums) != 1 || got.Enums[0].Name != "order_status" || !reflect.DeepEqual(got.Enums[0].Values, want) {
		t.Fatalf("unexpected enums: %+v", got.Enums)
	}
}

func TestScaffoldView(t *testing.T) {
	dir := scaffoldDir(t)
	sqlFile := filepath.Join(t.TempDir(), "definition.sql")
	if err := os.WriteFile(sqlFile, []byte("SELECT id FROM users"), 0644); err != nil {
		t.Fatal(err)
	}
	if code, diagnostics := scaffoldRun(t, "view", "-dir", dir, "active_users", sqlFile); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	if code, diagnostics := scaffoldRun(t, "view", "-dir", dir, "-materialized", "user_totals", sqlFile); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Views) != 2 || got.Views[0].Name != "active_users" || got.Views[0].Definition != "SELECT id FROM users" || got.Views[0].Materialized || got.Views[1].Name != "user_totals" || !got.Views[1].Materialized {
		t.Fatalf("unexpected views: %+v", got.Views)
	}
	path := filepath.Join(dir, "views", "active_users.sql")
	if err := os.WriteFile(path, []byte("SELECT id, email FROM users"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = schemigo.Load(t.Context(), dir)
	if err != nil || got.Views[0].Definition != "SELECT id, email FROM users" {
		t.Fatalf("SQL edit was not loaded: views=%+v err=%v", got.Views, err)
	}
}

func TestScaffoldRejectsDuplicateAndMalformedInput(t *testing.T) {
	dir := scaffoldDir(t)
	if code, diagnostics := scaffoldRun(t, "table", "-dir", dir, "users", "id:int8"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	entity := filepath.Join(dir, "tables", "users_schema.go")
	all := filepath.Join(dir, "tables", "all.go")
	beforeEntity, err := os.ReadFile(entity)
	if err != nil {
		t.Fatal(err)
	}
	beforeAll, err := os.ReadFile(all)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"table", "-dir", dir, "users", "id:text"},
		{"table", "-dir", dir, "orders", "amount:numeric(10,2"},
		{"table", "-dir", dir, "orders", "id"},
		{"enum", "-dir", dir, "status", "ready,,done"},
	} {
		if code, _ := scaffoldRun(t, args...); code != 1 {
			t.Fatalf("args=%v code=%d, want 1", args, code)
		}
	}
	afterEntity, _ := os.ReadFile(entity)
	afterAll, _ := os.ReadFile(all)
	if !bytes.Equal(beforeEntity, afterEntity) || !bytes.Equal(beforeAll, afterAll) {
		t.Fatal("failed scaffold changed existing files")
	}
	if _, err := os.Stat(filepath.Join(dir, "tables", "orders_schema.go")); !os.IsNotExist(err) {
		t.Fatalf("failed scaffold created an entity: %v", err)
	}
}

func TestScaffoldRejectsEmptySQL(t *testing.T) {
	dir := scaffoldDir(t)
	sqlFile := filepath.Join(t.TempDir(), "empty.sql")
	if err := os.WriteFile(sqlFile, []byte("  \n"), 0644); err != nil {
		t.Fatal(err)
	}
	if code, _ := scaffoldRun(t, "view", "-dir", dir, "empty_view", sqlFile); code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("failed scaffold created schema directory: %v", err)
	}
}

func TestScaffoldKeepsEditedSchemaFiles(t *testing.T) {
	dir := scaffoldDir(t)
	if code, diagnostics := scaffoldRun(t, "table", "-dir", dir, "users", "id:int8"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	entity := filepath.Join(dir, "tables", "users_schema.go")
	data, err := os.ReadFile(entity)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(data, []byte("Name: \"users\""), []byte("Name: \"people\""), 1)
	if bytes.Equal(data, edited) {
		t.Fatal("fixture did not change the entity")
	}
	if err := os.WriteFile(entity, edited, 0644); err != nil {
		t.Fatal(err)
	}
	all := filepath.Join(dir, "tables", "all.go")
	allData, err := os.ReadFile(all)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(all, append([]byte("// Custom schema list.\n"), allData...), 0644); err != nil {
		t.Fatal(err)
	}
	if code, diagnostics := scaffoldRun(t, "table", "-dir", dir, "orders", "id:int8"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tables) != 2 || got.Tables[0].Name != "people" || got.Tables[1].Name != "orders" {
		t.Fatalf("edited schema was lost: %+v", got.Tables)
	}
	updated, err := os.ReadFile(all)
	if err != nil || !bytes.Contains(updated, []byte("Custom schema list")) {
		t.Fatalf("all.go comment was lost: %v", err)
	}
}

func TestScaffoldInitializesEmptyDirectory(t *testing.T) {
	dir := scaffoldDir(t)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if code, diagnostics := scaffoldRun(t, "table", "-dir", dir, "users", "id:int8"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil || len(got.Tables) != 1 || got.Tables[0].Name != "users" {
		t.Fatalf("empty directory was not initialized: schema=%+v err=%v", got, err)
	}
}

func TestScaffoldRejectsGoSymbolCollision(t *testing.T) {
	dir := scaffoldDir(t)
	if code, diagnostics := scaffoldRun(t, "table", "-dir", dir, "foo_bar", "id:int8"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, diagnostics)
	}
	if code, _ := scaffoldRun(t, "table", "-dir", dir, "foo__bar", "id:int8"); code != 1 {
		t.Fatalf("colliding symbol accepted: code=%d", code)
	}
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil || len(got.Tables) != 1 || got.Tables[0].Name != "foo_bar" {
		t.Fatalf("collision damaged schema: schema=%+v err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tables", "foo__bar_schema.go")); !os.IsNotExist(err) {
		t.Fatalf("collision left a file: %v", err)
	}
}
