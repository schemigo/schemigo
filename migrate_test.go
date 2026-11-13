package schemigo_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/schemigo/schemigo"
)

type noopMigrationDriver struct {
	schemigo.Driver
	steps *[]string
}

func (d noopMigrationDriver) Lock(context.Context, *sql.Conn, string) (schemigo.UnlockFunc, error) {
	*d.steps = append(*d.steps, "lock")
	return func(context.Context) error { *d.steps = append(*d.steps, "unlock"); return nil }, nil
}
func (d noopMigrationDriver) Inspect(context.Context, *sql.Conn, string) (schemigo.Schema, error) {
	*d.steps = append(*d.steps, "inspect")
	return schemigo.Schema{Name: "public"}, nil
}
func (d noopMigrationDriver) Diff(context.Context, *sql.Conn, schemigo.Schema, schemigo.Schema) (schemigo.Plan, error) {
	*d.steps = append(*d.steps, "diff")
	return schemigo.Plan{}, nil
}

func TestApplyEmptyPlan(t *testing.T) {
	db := sql.OpenDB(exportConnector{})
	t.Cleanup(func() { db.Close() })
	var steps []string
	err := schemigo.Apply(t.Context(), db, noopMigrationDriver{steps: &steps}, "public", schemigo.Schema{Name: "public"}, schemigo.ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(steps); got != 4 || steps[0] != "lock" || steps[1] != "inspect" || steps[2] != "diff" || steps[3] != "unlock" {
		t.Fatalf("unexpected lifecycle: %v", steps)
	}
}
