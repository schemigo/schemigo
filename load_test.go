package schemigo_test

import (
	"context"
	"errors"
	"github.com/schemigo/schemigo"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoadCancellationStopsSchemaProcess(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	program := "package main\nimport (\"net\";\"io\";\"os\")\nfunc main(){c,e:=net.Dial(\"tcp\"," + strconv.Quote(listener.Addr().String()) + ");if e!=nil{os.Exit(1)};defer c.Close();c.Write([]byte{1});io.Copy(io.Discard,c)}"
	dir := writeSchemaProgram(t, program)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := schemigo.Load(ctx, dir); result <- err }()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var ready [1]byte
	if _, err := io.ReadFull(connection, ready[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Load did not return after cancellation")
	}
	// The direct child must exit too; merely killing a go-run parent is insufficient.
	_, err = connection.Read(ready[:])
	if err == nil {
		t.Fatal("schema process remains alive")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("cancelled schema kept its connection open")
	}
}
func TestLoadRejectsInvalidSchema(t *testing.T) {
	dir := writeSchemaProgram(t, "package main\nimport \"fmt\"\nfunc main(){fmt.Print("+strconv.Quote(`{"order":["unknown"]}`)+")}")
	if _, err := schemigo.Load(t.Context(), dir); err == nil {
		t.Fatal("invalid schema accepted")
	}
}

func writeSchemaProgram(t *testing.T, body string) string {
	t.Helper()
	dir := schemaDirectory(t)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestLoadReportsGoCompileError(t *testing.T) {
	dir := schemaDirectory(t)
	if err := exportFixture(t, testSchema(), dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tables", "broken.go"), []byte("package tables\nvar Broken ="), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := schemigo.Load(t.Context(), dir); err == nil || !strings.Contains(err.Error(), "broken.go") {
		t.Fatalf("missing compiler diagnostic: %v", err)
	}
}
func TestLoadRejectsExtraJSON(t *testing.T) {
	for _, output := range []string{"{} {}", "{} trailing", "null", "[]", "not json"} {
		t.Run(output, func(t *testing.T) {
			dir := writeSchemaProgram(t, "package main\nimport \"fmt\"\nfunc main(){fmt.Print("+strconv.Quote(output)+")}")
			if _, err := schemigo.Load(t.Context(), dir); err == nil {
				t.Fatalf("accepted %q", output)
			}
		})
	}
}
func TestLoadSeparatesStderr(t *testing.T) {
	dir := writeSchemaProgram(t, `package main
import ("fmt";"os")
func main(){fmt.Fprintln(os.Stderr,"diagnostic");fmt.Print("{\"name\":\"public\"}")}`)
	got, err := schemigo.Load(t.Context(), dir)
	if err != nil || got.Name != "public" {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestLoadExecutionError(t *testing.T) {
	dir := writeSchemaProgram(t, `package main
import ("fmt";"os")
func main(){fmt.Fprintln(os.Stderr,"schema failed");os.Exit(7)}`)
	if _, err := schemigo.Load(t.Context(), dir); err == nil || !strings.Contains(err.Error(), "schema failed") {
		t.Fatal(err)
	}
}
func TestLoadCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := schemigo.Load(ctx, schemaDirectory(t)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLoadWithinMultiModuleWorkspace(t *testing.T) {
	root := t.TempDir()
	// Disposable module files exercise discovery only; they have no dependencies
	// and never alter the repository's module or bypass its version requirement.
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "go.mod"), []byte("module example.com/"+name+"\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.27\nuse (\n./one\n./two\n)\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "one", "schema")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Print(\"{}\")}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))
	t.Chdir(filepath.Join(root, "one"))
	if _, err := schemigo.Load(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsOutsideModule(t *testing.T) {
	if _, err := schemigo.Load(t.Context(), t.TempDir()); err == nil {
		t.Fatal("outside module accepted")
	}
}
