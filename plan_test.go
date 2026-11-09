package schemigo_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/schemigo/schemigo"
)

func TestPlanHash(t *testing.T) {
	p := schemigo.Plan{Statements: []schemigo.Statement{{SQL: `CREATE TABLE "public"."users" (id int8)`}, {SQL: `DROP TABLE "public"."old";`, Destructive: true, SkipExplain: true}}}
	want := "CREATE TABLE \"public\".\"users\" (id int8);\nDROP TABLE \"public\".\"old\";\n"
	if p.SQL() != want {
		t.Fatalf("SQL = %q, want %q", p.SQL(), want)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(want)))
	if p.SHA256() != hash || p.SHA256() != p.SHA256() {
		t.Fatal("unstable or incorrect hash")
	}
	if (schemigo.Plan{}).SQL() != "" {
		t.Fatal("empty plan must produce empty SQL")
	}
	other := p
	other.Statements = append([]schemigo.Statement(nil), p.Statements...)
	other.Statements[0].SQL += " -- changed"
	if other.SHA256() == p.SHA256() {
		t.Fatal("SQL change did not change hash")
	}
}

func TestPlanSchemaQualification(t *testing.T) {
	p := schemigo.Plan{Statements: []schemigo.Statement{
		{SQL: `CREATE TABLE "first"."users" (id int8)`},
		{SQL: `CREATE TABLE "second"."users" (id int8)`},
	}}
	want := "CREATE TABLE \"first\".\"users\" (id int8);\nCREATE TABLE \"second\".\"users\" (id int8);\n"
	if p.SQL() != want {
		t.Fatalf("qualified SQL changed: %q", p.SQL())
	}
}

func TestPlanSQLPreservesBody(t *testing.T) {
	p := schemigo.Plan{Statements: []schemigo.Statement{
		{SQL: " \n"},
		{SQL: "  DO $$ BEGIN PERFORM 1; END $$;\n"},
	}}
	if got := p.SQL(); got != "DO $$ BEGIN PERFORM 1; END $$;\n" {
		t.Fatalf("SQL = %q", got)
	}
}
