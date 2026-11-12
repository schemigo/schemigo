package schemigo_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/schemigo/schemigo"
	"go/format"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type exportDriver struct {
	schemigo.Driver
	schema schemigo.Schema
	err    error
}

func (d exportDriver) Inspect(ctx context.Context, conn *sql.Conn, name string) (schemigo.Schema, error) {
	if err := ctx.Err(); err != nil {
		return schemigo.Schema{}, err
	}
	if conn == nil || name != d.schema.Name {
		return schemigo.Schema{}, errors.New("wrong inspection arguments")
	}
	return d.schema, d.err
}

type exportConnector struct{}

func (exportConnector) Connect(context.Context) (driver.Conn, error) { return exportConn{}, nil }
func (exportConnector) Driver() driver.Driver                        { return exportSQLDriver{} }

type exportSQLDriver struct{}

func (exportSQLDriver) Open(string) (driver.Conn, error) { return exportConn{}, nil }

type exportConn struct{}

func (exportConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected SQL") }
func (exportConn) Close() error                        { return nil }
func (exportConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected transaction") }
func exportFixture(t *testing.T, s schemigo.Schema, dir string) error {
	t.Helper()
	db := sql.OpenDB(exportConnector{})
	t.Cleanup(func() { db.Close() })
	return schemigo.Export(t.Context(), db, exportDriver{schema: s}, s.Name, dir)
}
func schemaDirectory(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "_schemigo_test_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return filepath.Join(dir, "schema")
}
func TestExportLoadRoundTrip(t *testing.T) {
	s := testSchema()
	s.Extensions[0].Version = "1.99"
	s.Tables[0].Columns[0].Default = "nextval('users_id_seq'::regclass)"
	s.Tables[0].Columns[1].Using = "parent::bigint"
	s.Tables[0].Engine = "MergeTree()"
	s.Tables[0].OrderBy = "id"
	s.Tables[0].PartitionBy = "parent"
	s.Tables[0].Indexes = append(s.Tables[0].Indexes, schemigo.Index{Name: "expr", Expression: "lower('X')", Where: "id > 0", Unique: true, Method: "gin", Type: "minmax", Granularity: 4})
	s.Functions[0].Body = "BEGIN\n-- Unicode: Привет, \x60quoted\x60\nRETURN NEW;\nEND"
	s.Views[0].Materialized = true
	dir := schemaDirectory(t)
	if err := exportFixture(t, s, dir); err != nil {
		t.Fatal(err)
	}
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := schemigo.Normalize(s)
	want.Extensions[0].Version = ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed schema:\ngot %+v\nwant %+v", got, want)
	}
	if s.Extensions[0].Version != "1.99" {
		t.Fatal("export mutated input")
	}
	for _, pkg := range []string{"extensions", "enums", "tables", "functions", "triggers", "views"} {
		for _, file := range []string{"types.go", "all.go"} {
			if _, err := os.Stat(filepath.Join(dir, pkg, file)); err != nil {
				t.Fatal(err)
			}
		}
	}
	files := sourceFiles(t, dir)
	for name, data := range files {
		formatted, err := format.Source(data)
		if err != nil || string(data) != string(formatted) {
			t.Fatalf("%s is not formatted: %v", name, err)
		}
		if strings.Contains(string(data), "1.99") {
			t.Fatalf("extension version exported in %s", name)
		}
	}
	other := schemaDirectory(t)
	if err := exportFixture(t, s, other); err != nil {
		t.Fatal(err)
	}
	otherFiles := sourceFiles(t, other)
	delete(files, "main.go")
	delete(otherFiles, "main.go")
	if !reflect.DeepEqual(files, otherFiles) {
		t.Fatal("export is not deterministic")
	}
}
func sourceFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		files[relative] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
func TestExportSpecialNames(t *testing.T) {
	s := schemigo.Schema{Name: "public"}
	for _, name := range []string{"users", "Users", "all", "types", "Table", "CON", "x/y", "..\\escape", "a-b", "a_b", "中文", "x_test", "x_linux", "x_amd64"} {
		s.Tables = append(s.Tables, schemigo.Table{Name: name, Columns: []schemigo.Column{{Name: "id", Type: "bigint"}}})
	}
	s.Functions = []schemigo.Function{{Name: "echo", Parameters: "x text"}, {Name: "echo", Parameters: "x bigint"}}
	dir := schemaDirectory(t)
	if err := exportFixture(t, s, dir); err != nil {
		t.Fatal(err)
	}
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := schemigo.Normalize(s)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("special names or overloads lost")
	}
	seen := map[string]bool{}
	for path := range sourceFiles(t, dir) {
		key := strings.ToLower(path)
		if seen[key] {
			t.Fatal("case insensitive collision", path)
		}
		seen[key] = true
	}
}
func TestExportRefusesOverwrite(t *testing.T) {
	dir := schemaDirectory(t)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keep.go")
	if err := os.WriteFile(path, []byte("my schema"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exportFixture(t, testSchema(), dir); err == nil {
		t.Fatal("nonempty directory overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "my schema" {
		t.Fatal("existing file changed")
	}
}
func TestExportFailureLeavesNoOutput(t *testing.T) {
	dir := schemaDirectory(t)
	cause := errors.New("inspection failed")
	s := testSchema()
	db := sql.OpenDB(exportConnector{})
	defer db.Close()
	err := schemigo.Export(t.Context(), db, exportDriver{schema: s, err: cause}, s.Name, dir)
	if !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed export left output", err)
	}
}

func TestExportRejectsInvalidImportDirectory(t *testing.T) {
	dir := schemaDirectory(t)
	dir = filepath.Join(filepath.Dir(dir), "схема")
	if err := exportFixture(t, testSchema(), dir); err == nil {
		t.Fatal("uncompilable Unicode import directory accepted")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid export created output")
	}
}

func TestExportOutsideModule(t *testing.T) {
	if err := exportFixture(t, testSchema(), filepath.Join(t.TempDir(), "schema")); err == nil {
		t.Fatal("outside module accepted")
	}
}
