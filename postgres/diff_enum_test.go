package postgres_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/postgres"
)

func enumSchema(values ...string) schemigo.Schema {
	s := schemigo.Schema{Name: "public"}
	if len(values) > 0 {
		s.Enums = []schemigo.Enum{{Name: "status", Values: values}}
	}
	return s
}

func TestDiffEnumAdd(t *testing.T) {
	old := enumSchema("pending", "cancelled")
	desired := enumSchema("first", "pending", "refunded", "cancelled", "last")
	before := append([]string(nil), desired.Enums[0].Values...)
	p, err := postgres.New().Diff(t.Context(), nil, old, desired)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`ALTER TYPE "public"."status" ADD VALUE IF NOT EXISTS 'first' BEFORE 'pending'`,
		`ALTER TYPE "public"."status" ADD VALUE IF NOT EXISTS 'refunded' BEFORE 'cancelled'`,
		`ALTER TYPE "public"."status" ADD VALUE IF NOT EXISTS 'last' AFTER 'cancelled'`,
	}
	if len(p.Statements) != len(want) {
		t.Fatalf("plan=%+v", p)
	}
	for i, s := range p.Statements {
		if s.SQL != want[i] || !s.SkipExplain || !s.OutsideTransaction {
			t.Fatalf("statement=%+v, want %q", s, want[i])
		}
	}
	if !reflect.DeepEqual(before, desired.Enums[0].Values) {
		t.Fatal("Diff mutated schema")
	}
	again, err := postgres.New().Diff(t.Context(), nil, old, desired)
	if err != nil || again.SQL() != p.SQL() {
		t.Fatalf("unstable plan: %v", err)
	}
}

func TestDiffEnumCreateAndDrop(t *testing.T) {
	p, err := postgres.New().Diff(t.Context(), nil, enumSchema(), enumSchema("a'b", `x\y`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Statements) != 1 || !strings.Contains(p.SQL(), `CREATE TYPE "public"."status" AS ENUM`) {
		t.Fatalf("plan=%+v", p)
	}
	p, err = postgres.New().Diff(t.Context(), nil, enumSchema("a"), enumSchema())
	if err != nil || len(p.Statements) != 1 || p.Statements[0].SQL != `DROP TYPE IF EXISTS "public"."status"` || !p.Statements[0].Destructive {
		t.Fatalf("plan=%+v, err=%v", p, err)
	}
	p, err = postgres.New().Diff(t.Context(), nil, enumSchema("a"), enumSchema("a"))
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("plan=%+v, err=%v", p, err)
	}
}

func TestDiffExtension(t *testing.T) {
	old := schemigo.Schema{Name: "public", Extensions: []schemigo.Extension{{Name: "old", Version: "1"}, {Name: "pinned", Version: "1"}}}
	desired := schemigo.Schema{Name: "public", Extensions: []schemigo.Extension{{Name: "new"}, {Name: "pinned", Version: "2"}}}
	p, err := postgres.New().Diff(t.Context(), nil, old, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Statements) != 2 || strings.Contains(p.SQL(), "DROP EXTENSION") || !strings.Contains(p.SQL(), `CREATE EXTENSION IF NOT EXISTS "new"`) || !strings.Contains(p.SQL(), `ALTER EXTENSION "pinned" UPDATE TO`) {
		t.Fatalf("plan=%+v", p)
	}
	desired.Extensions = []schemigo.Extension{{Name: "pinned"}}
	p, err = postgres.New().Diff(t.Context(), nil, old, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("unpin/removal plan=%+v, err=%v", p, err)
	}
}

func TestDiffRejectsUnsupported(t *testing.T) {
	old := enumSchema("a")
	desired := enumSchema("a", "b")
	desired.Views = []schemigo.View{{Name: "v", Definition: "SELECT 1"}}
	p, err := postgres.New().Diff(t.Context(), nil, old, desired)
	if !errors.Is(err, errors.ErrUnsupported) || len(p.Statements) != 0 {
		t.Fatalf("plan=%+v, err=%v", p, err)
	}
	desired = enumSchema("a")
	desired.Name = "other"
	if _, err := postgres.New().Diff(t.Context(), nil, old, desired); err == nil {
		t.Fatal("mixed schemas accepted")
	}
	desired = enumSchema("a\x00b")
	if _, err := postgres.New().Diff(t.Context(), nil, old, desired); err == nil {
		t.Fatal("NUL label accepted")
	}
	if _, err := postgres.New().Diff(t.Context(), nil, enumSchema("a", "b"), enumSchema("b")); err == nil {
		t.Fatal("rebuild without catalog connection accepted")
	}
}

func TestDiffEmptySlices(t *testing.T) {
	current := schemigo.Schema{Name: "public"}
	desired := schemigo.Schema{Name: "public", Tables: []schemigo.Table{}, Views: []schemigo.View{}, Functions: []schemigo.Function{}, Triggers: []schemigo.Trigger{}}
	p, err := postgres.New().Diff(t.Context(), nil, current, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("empty slices changed schema: %+v, %v", p, err)
	}
	current.Tables = []schemigo.Table{{Name: "t", Columns: []schemigo.Column{{Name: "id", Type: "text"}}, Indexes: []schemigo.Index{{Name: "expr", Expression: "lower(id)"}}}}
	desired.Tables = []schemigo.Table{{Name: "t", Columns: []schemigo.Column{{Name: "id", Type: "text"}}, PrimaryKey: []string{}, ForeignKeys: []schemigo.ForeignKey{}, Checks: []schemigo.Check{}, Indexes: []schemigo.Index{{Name: "expr", Expression: "lower(id)", Columns: []string{}}}}}
	p, err = postgres.New().Diff(t.Context(), nil, current, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("nested empty slices changed schema: %+v, %v", p, err)
	}
}
