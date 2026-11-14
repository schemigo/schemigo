package clickhouse

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
)

func exampleSchema() schemigo.Schema {
	return schemigo.Schema{Name: "analytics", Tables: []schemigo.Table{{Name: "events", Engine: "MergeTree", OrderBy: "id", Columns: []schemigo.Column{{Name: "id", Type: UInt64}, {Name: "body", Type: String}}, Indexes: []schemigo.Index{{Name: "body_idx", Columns: []string{"body"}, Type: "bloom_filter", Granularity: 1}}}}}
}
func TestClickHouseTableDiff(t *testing.T) {
	d := New(nil)
	want := exampleSchema()
	p, err := d.Diff(t.Context(), nil, schemigo.Schema{Name: want.Name}, want)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"CREATE TABLE IF NOT EXISTS `analytics`.`events`", "ENGINE = MergeTree", "ORDER BY id", "INDEX `body_idx` `body` TYPE bloom_filter GRANULARITY 1"} {
		if !strings.Contains(p.SQL(), part) {
			t.Errorf("missing %q: %s", part, p.SQL())
		}
	}
	for _, s := range p.Statements {
		if !s.OutsideTransaction || !s.SkipExplain {
			t.Fatal("ClickHouse DDL must run outside transactions")
		}
	}
	current := exampleSchema()
	want.Tables[0].Columns = []schemigo.Column{{Name: "id", Type: UInt32}, {Name: "added", Type: String}}
	want.Tables[0].Indexes = nil
	p, err = d.Diff(t.Context(), nil, current, want)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"MODIFY COLUMN IF EXISTS `id` UInt32", "ADD COLUMN IF NOT EXISTS `added` String", "DROP COLUMN IF EXISTS `body`", "DROP INDEX IF EXISTS `body_idx`"} {
		if !strings.Contains(p.SQL(), part) {
			t.Errorf("missing %q: %s", part, p.SQL())
		}
	}
	p, err = d.Diff(t.Context(), nil, current, current)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("self diff: %s %v", p.SQL(), err)
	}
}
func TestClickHouseMaterializedViewDiff(t *testing.T) {
	a := schemigo.Schema{Name: "analytics", Views: []schemigo.View{{Name: "mv", Materialized: true, Definition: "ENGINE = MergeTree ORDER BY id AS SELECT 1 AS id"}}}
	b := a
	b.Views = []schemigo.View{{Name: "mv", Materialized: true, Definition: "ENGINE = MergeTree ORDER BY id AS SELECT 2 AS id"}}
	p, err := New(nil).Diff(t.Context(), nil, a, b)
	if err != nil || len(p.Statements) != 2 || !p.Statements[0].Destructive || !strings.Contains(p.SQL(), "CREATE MATERIALIZED VIEW IF NOT EXISTS") {
		t.Fatalf("%s %v", p.SQL(), err)
	}
}
func TestClickHouseRejectsUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*schemigo.Schema)
	}{
		{"extension", func(s *schemigo.Schema) { s.Extensions = []schemigo.Extension{{Name: "x"}} }},
		{"enum", func(s *schemigo.Schema) { s.Enums = []schemigo.Enum{{Name: "e", Values: []string{"x"}}} }},
		{"trigger", func(s *schemigo.Schema) { s.Triggers = []schemigo.Trigger{{Name: "t"}} }},
		{"foreign key", func(s *schemigo.Schema) { s.Tables[0].ForeignKeys = []schemigo.ForeignKey{{Name: "fk"}} }},
		{"no engine", func(s *schemigo.Schema) { s.Tables[0].Engine = "" }},
		{"reserved history", func(s *schemigo.Schema) { s.Tables[0].Name = "schema_migrations" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := exampleSchema()
			tc.change(&s)
			if _, err := New(nil).Diff(t.Context(), nil, schemigo.Schema{Name: s.Name}, s); err == nil {
				t.Fatal("accepted unsupported schema")
			}
		})
	}
}
func TestClickHouseLockWithoutKeeper(t *testing.T) {
	if _, err := New(nil).Lock(t.Context(), nil, "analytics"); err == nil {
		t.Fatal("lock without Keeper succeeded")
	}
}

func TestClickHouseFunctionDependencies(t *testing.T) {
	s := exampleSchema()
	s.Functions = []schemigo.Function{{Name: "first", Parameters: "x", Body: "second(x) + 1"}, {Name: "second", Parameters: "x", Body: "x * 2"}}
	s.Tables[0].Columns[0].Default = "first(1)"
	p, err := New(nil).Diff(t.Context(), nil, schemigo.Schema{Name: s.Name}, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Statements) != 3 || !strings.Contains(p.Statements[0].SQL, "FUNCTION `second`") || !strings.Contains(p.Statements[1].SQL, "FUNCTION `first`") || !strings.Contains(p.Statements[2].SQL, "CREATE TABLE") {
		t.Fatalf("dependency order: %s", p.SQL())
	}
	s.Functions[1].Body = "first(x)"
	if _, err := New(nil).Diff(t.Context(), nil, schemigo.Schema{Name: s.Name}, s); err == nil {
		t.Fatal("function cycle accepted")
	}
}
func TestClickHouseViewDependencies(t *testing.T) {
	s := schemigo.Schema{Name: "db", Views: []schemigo.View{{Name: "first", Definition: "SELECT id FROM second"}, {Name: "second", Definition: "SELECT 1 AS id"}}}
	p, err := New(nil).Diff(t.Context(), nil, schemigo.Schema{Name: s.Name}, s)
	if err != nil || len(p.Statements) != 2 || !strings.Contains(p.Statements[0].SQL, "`db`.`second`") {
		t.Fatalf("dependency order: %s %v", p.SQL(), err)
	}
	s.Views[1].Definition = "SELECT id FROM first"
	if _, err := New(nil).Diff(t.Context(), nil, schemigo.Schema{Name: s.Name}, s); err == nil {
		t.Fatal("view cycle accepted")
	}
}

func TestClickHouseImplicitPrimaryKey(t *testing.T) {
	current := exampleSchema()
	desired := exampleSchema()
	desired.Tables[0].PrimaryKey = []string{"id"}
	p, err := New(nil).Diff(t.Context(), nil, current, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("redundant primary key diff: %s %v", p.SQL(), err)
	}
	if len(desired.Tables[0].PrimaryKey) != 1 {
		t.Fatal("mutated desired primary key")
	}
}
