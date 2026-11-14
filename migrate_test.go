package schemigo_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
)

type partialConnector struct{ executed *int }

func (c partialConnector) Connect(context.Context) (driver.Conn, error) {
	return partialConn{executed: c.executed}, nil
}
func (partialConnector) Driver() driver.Driver { return partialSQLDriver{} }

type partialSQLDriver struct{}

func (partialSQLDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unexpected open") }

type partialConn struct{ executed *int }

func (partialConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (partialConn) Close() error                        { return nil }
func (partialConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected transaction") }
func (c partialConn) ExecContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	*c.executed++
	if *c.executed == 2 {
		return nil, errors.New("second DDL failed")
	}
	return driver.RowsAffected(1), nil
}

type partialMigrationDriver struct{ noopMigrationDriver }

func (partialMigrationDriver) Diff(context.Context, *sql.Conn, schemigo.Schema, schemigo.Schema) (schemigo.Plan, error) {
	return schemigo.Plan{Statements: []schemigo.Statement{{SQL: "CREATE TABLE a (id INT)", OutsideTransaction: true}, {SQL: "CREATE TABLE b (id INT)", OutsideTransaction: true}}}, nil
}
func (partialMigrationDriver) Validate(context.Context, *sql.Conn, schemigo.Statement) error {
	return nil
}
func (partialMigrationDriver) EnsureHistory(context.Context, *sql.Conn, string) error { return nil }

func TestApplyReportsCommittedStatementsAfterFailure(t *testing.T) {
	executed := 0
	db := sql.OpenDB(partialConnector{executed: &executed})
	defer db.Close()
	var steps []string
	err := schemigo.Apply(t.Context(), db, partialMigrationDriver{noopMigrationDriver{steps: &steps}}, "public", schemigo.Schema{Name: "public"}, schemigo.ApplyOptions{NoBackup: true})
	var failure *schemigo.ApplyError
	if !errors.As(err, &failure) || failure.CompletedStatements != 1 || !strings.Contains(err.Error(), "1 statement") {
		t.Fatalf("partial DDL was not reported: %v", err)
	}
}

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
