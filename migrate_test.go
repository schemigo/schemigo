package schemigo_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
)

type changedPlanDriver struct{ noopMigrationDriver }

func (d changedPlanDriver) Diff(context.Context, *sql.Conn, schemigo.Schema, schemigo.Schema) (schemigo.Plan, error) {
	*d.steps = append(*d.steps, "diff")
	return schemigo.Plan{Statements: []schemigo.Statement{{SQL: "SELECT 1"}}}, nil
}

func TestApplyRejectsChangedPreview(t *testing.T) {
	db := sql.OpenDB(exportConnector{})
	t.Cleanup(func() { db.Close() })
	var steps []string
	err := schemigo.Apply(t.Context(), db, changedPlanDriver{noopMigrationDriver{steps: &steps}}, "public", schemigo.Schema{Name: "public"}, schemigo.ApplyOptions{NoBackup: true, ExpectedHash: "earlier-preview"})
	if err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatalf("changed plan was accepted: %v", err)
	}
	if strings.Join(steps, ",") != "lock,inspect,diff,unlock" {
		t.Fatalf("changed plan reached validation or SQL: %v", steps)
	}
}

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
