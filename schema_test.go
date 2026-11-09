package schemigo_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
)

func testSchema() schemigo.Schema {
	return schemigo.Schema{
		Name:       "public",
		Extensions: []schemigo.Extension{{Name: "pg_trgm"}},
		Enums:      []schemigo.Enum{{Name: "status", Values: []string{"", "active"}}},
		Tables:     []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "int8"}, {Name: "parent", Type: "int8"}}, PrimaryKey: []string{"id", "parent"}, Indexes: []schemigo.Index{{Name: "idx", Columns: []string{"parent"}}}, ForeignKeys: []schemigo.ForeignKey{{Name: "fk", Columns: []string{"parent"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}, Checks: []schemigo.Check{{Name: "positive", Expression: "id > 0"}}}},
		Functions:  []schemigo.Function{{Name: "touch", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END"}},
		Triggers:   []schemigo.Trigger{{Name: "touch", Table: "users", When: "BEFORE", Event: "UPDATE", Function: "touch"}},
		Views:      []schemigo.View{{Name: "active", Definition: "SELECT * FROM users"}},
		Order:      []schemigo.EntityKind{schemigo.EntityEnums},
	}
}

func TestNormalize(t *testing.T) {
	input := testSchema()
	before, _ := json.Marshal(input)
	got, err := schemigo.Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tables[0].Indexes[0].Method != "btree" || got.Triggers[0].ForEach != "ROW" || !got.Triggers[0].ForEachRow {
		t.Fatalf("missing defaults: %+v", got)
	}
	if !reflect.DeepEqual(got.Tables[0].PrimaryKey, []string{"id", "parent"}) {
		t.Fatal("primary key order changed")
	}
	again, err := schemigo.Normalize(got)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("normalization is not idempotent: %v", err)
	}
	got.Extensions[0].Name = "changed"
	got.Enums[0].Values[0] = "changed"
	got.Tables[0].Columns[0].Name = "changed"
	got.Tables[0].PrimaryKey[0] = "changed"
	got.Tables[0].Indexes[0].Columns[0] = "changed"
	got.Tables[0].ForeignKeys[0].Columns[0] = "changed"
	got.Tables[0].ForeignKeys[0].ReferencedColumns[0] = "changed"
	got.Tables[0].Checks[0].Name = "changed"
	got.Functions[0].Name = "changed"
	got.Triggers[0].Name = "changed"
	got.Views[0].Name = "changed"
	got.Order[0] = schemigo.EntityViews
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("Normalize aliases or mutates input")
	}
	empty, err := schemigo.Normalize(schemigo.Schema{})
	if err != nil || len(empty.Order) != 6 {
		t.Fatalf("zero schema: %+v, %v", empty, err)
	}
}

func TestNormalizeRejectsInvalidSchema(t *testing.T) {
	tests := []struct {
		name   string
		change func(*schemigo.Schema)
		want   string
	}{
		{"duplicate table", func(s *schemigo.Schema) { s.Tables = append(s.Tables, s.Tables[0]) }, "users"},
		{"empty name", func(s *schemigo.Schema) { s.Tables[0].Name = "" }, "name"},
		{"nul name", func(s *schemigo.Schema) { s.Name = "bad\x00name" }, "name"},
		{"duplicate column", func(s *schemigo.Schema) { s.Tables[0].Columns[1].Name = "id" }, "id"},
		{"missing type", func(s *schemigo.Schema) { s.Tables[0].Columns[0].Type = "" }, "type"},
		{"index alternatives", func(s *schemigo.Schema) { s.Tables[0].Indexes[0].Expression = "lower(parent)" }, "index"},
		{"empty index", func(s *schemigo.Schema) { s.Tables[0].Indexes[0].Columns = nil }, "index"},
		{"missing column", func(s *schemigo.Schema) { s.Tables[0].Indexes[0].Columns[0] = "absent" }, "absent"},
		{"foreign key arity", func(s *schemigo.Schema) { s.Tables[0].ForeignKeys[0].ReferencedColumns = nil }, "foreign key"},
		{"missing referenced table", func(s *schemigo.Schema) { s.Tables[0].ForeignKeys[0].ReferencedTable = "absent" }, "absent"},
		{"missing referenced column", func(s *schemigo.Schema) { s.Tables[0].ForeignKeys[0].ReferencedColumns[0] = "absent" }, "absent"},
		{"conflicting primary key", func(s *schemigo.Schema) { s.Tables[0].Columns[0].PrimaryKey = true }, "primary key"},
		{"duplicate primary key column", func(s *schemigo.Schema) { s.Tables[0].PrimaryKey = []string{"id", "id"} }, "id"},
		{"duplicate enum value", func(s *schemigo.Schema) { s.Enums[0].Values = []string{"x", "x"} }, "enum"},
		{"empty enum", func(s *schemigo.Schema) { s.Enums[0].Values = nil }, "enum"},
		{"empty check", func(s *schemigo.Schema) { s.Tables[0].Checks[0].Expression = "" }, "check"},
		{"trigger conflict", func(s *schemigo.Schema) { s.Triggers[0].ForEach = "STATEMENT"; s.Triggers[0].ForEachRow = true }, "ForEach"},
		{"trigger mode", func(s *schemigo.Schema) { s.Triggers[0].ForEach = "OTHER" }, "ForEach"},
		{"trigger table", func(s *schemigo.Schema) { s.Triggers[0].Table = "absent" }, "absent"},
		{"trigger function", func(s *schemigo.Schema) { s.Triggers[0].Function = "absent" }, "absent"},
		{"duplicate order", func(s *schemigo.Schema) { s.Order = append(s.Order, s.Order[0]) }, "order"},
		{"unknown order", func(s *schemigo.Schema) { s.Order = []schemigo.EntityKind{"unknown"} }, "order"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSchema()
			tt.change(&s)
			_, err := schemigo.Normalize(s)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want context %q", err, tt.want)
			}
		})
	}
}

func TestNormalizeFunctionOverloads(t *testing.T) {
	s := testSchema()
	s.Functions = append(s.Functions, schemigo.Function{Name: "touch", Parameters: "id int8", Returns: "int8", Language: "sql", Body: "SELECT id"})
	if _, err := schemigo.Normalize(s); err != nil {
		t.Fatal(err)
	}
	s.Functions = append(s.Functions, s.Functions[1])
	if _, err := schemigo.Normalize(s); err == nil {
		t.Fatal("duplicate signature accepted")
	}
}

func TestSchemaJSON(t *testing.T) {
	s := testSchema()
	s.Tables[0].Engine = "MergeTree()"
	s.Tables[0].OrderBy = "id"
	s.Tables[0].PartitionBy = "parent"
	s.Tables[0].Indexes[0].Type = "minmax"
	s.Tables[0].Indexes[0].Granularity = 4
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var got schemigo.Schema
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, got) {
		t.Fatal("JSON round trip lost fields")
	}
	if !strings.Contains(string(data), `"referenced_columns"`) {
		t.Fatal("missing explicit JSON tags")
	}
}

func TestNormalizeIdentifiersAndStatementTrigger(t *testing.T) {
	s := testSchema()
	s.Name = `a.b"c`
	s.Triggers[0].ForEach = "STATEMENT"
	got, err := schemigo.Normalize(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != s.Name || got.Triggers[0].ForEachRow {
		t.Fatalf("unexpected normalization: %+v", got)
	}
	wantOrder := []schemigo.EntityKind{schemigo.EntityEnums, schemigo.EntityExtensions, schemigo.EntityTables, schemigo.EntityFunctions, schemigo.EntityTriggers, schemigo.EntityViews}
	if !reflect.DeepEqual(got.Order, wantOrder) {
		t.Fatalf("order = %v", got.Order)
	}
}

func ExampleNormalize() {
	s, err := schemigo.Normalize(schemigo.Schema{
		Name:   "public",
		Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "int8", PrimaryKey: true}}}},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(s.Name, s.Tables[0].Columns[0].Name)
	// Output: public id
}
