//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/internal/testdb"
	"github.com/schemigo/schemigo/postgres"
)

type failingHistoryDriver struct{ *postgres.Driver }

func (*failingHistoryDriver) RecordMigrationTx(context.Context, *sql.Tx, string, schemigo.Migration) error {
	return errors.New("history insert refused")
}

type failingUnlockDriver struct{ *postgres.Driver }

func (d *failingUnlockDriver) Lock(ctx context.Context, conn *sql.Conn, schema string) (schemigo.UnlockFunc, error) {
	unlock, err := d.Driver.Lock(ctx, conn, schema)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		if err := unlock(ctx); err != nil {
			return err
		}
		return errors.New("unlock refused")
	}, nil
}

func TestApplyKeepsSnapshotOnUnlockError(t *testing.T) {
	db := testdb.Postgres(t)
	backup, err := os.MkdirTemp(".", "_schemigo_backup_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(backup) })
	err = schemigo.Apply(t.Context(), db, &failingUnlockDriver{Driver: &postgres.Driver{}}, "public", tableSchema(simpleTable("users")), schemigo.ApplyOptions{BackupDir: backup})
	var applyErr *schemigo.ApplyError
	if !errors.As(err, &applyErr) || applyErr.BackupPath == "" || !strings.Contains(err.Error(), "unlock refused") {
		t.Fatalf("unlock error and snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(applyErr.BackupPath, "main.go")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyHistoryFailureRollsBackSQL(t *testing.T) {
	db := testdb.Postgres(t)
	desired := tableSchema(simpleTable("users"))
	err := schemigo.Apply(t.Context(), db, &failingHistoryDriver{Driver: &postgres.Driver{}}, "public", desired, schemigo.ApplyOptions{NoBackup: true})
	if err == nil || !strings.Contains(err.Error(), "history insert refused") {
		t.Fatalf("history error: %v", err)
	}
	var tableExists bool
	if err := db.QueryRowContext(t.Context(), `SELECT pg_catalog.to_regclass('public.users') IS NOT NULL`).Scan(&tableExists); err != nil || tableExists {
		t.Fatalf("unrecorded SQL committed: table=%v err=%v", tableExists, err)
	}
}

func TestApplyPostgresLifecycle(t *testing.T) {
	db := testdb.Postgres(t)
	desired := tableSchema(simpleTable("users"))
	driver := postgres.New()
	backup, err := os.MkdirTemp(".", "_schemigo_backup_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(backup) })
	if err := schemigo.Apply(t.Context(), db, driver, "public", desired, schemigo.ApplyOptions{BackupDir: backup}); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(backup); err != nil || len(entries) != 0 {
		t.Fatalf("successful snapshot cleanup: %v %v", entries, err)
	}
	if p, err := schemigo.PlanMigration(t.Context(), db, driver, "public", desired); err != nil || len(p.Statements) != 0 {
		t.Fatalf("plan after apply: %v %v", p, err)
	}
	if err := schemigo.Apply(t.Context(), db, driver, "public", desired, schemigo.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	history, err := schemigo.History(t.Context(), db, driver, "public")
	if err != nil || len(history) != 1 || history[0].Hash == "" || history[0].User == "" || history[0].AppliedAt.IsZero() {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	seen, err := driver.HasMigration(t.Context(), conn, "public", history[0].Hash)
	if err != nil || !seen {
		t.Fatalf("hash=%v err=%v", seen, err)
	}
	if _, err := db.ExecContext(t.Context(), `DROP TABLE public.users`); err != nil {
		t.Fatal(err)
	}
	if err := schemigo.Apply(t.Context(), db, driver, "public", desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	history, err = schemigo.History(t.Context(), db, driver, "public")
	if err != nil || len(history) != 2 || history[0].Hash != history[1].Hash {
		t.Fatalf("drift replay history=%+v err=%v", history, err)
	}
}

func TestApplyKeepsSnapshotAfterError(t *testing.T) {
	db := testdb.Postgres(t)
	backup, err := os.MkdirTemp(".", "_schemigo_backup_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(backup) })
	// A missing relation makes the desired view invalid at execution time.
	desired := tableSchema(simpleTable("users"))
	desired.Views = []schemigo.View{{Name: "broken", Definition: "SELECT id FROM nonexistent_table"}}
	err = schemigo.Apply(t.Context(), db, postgres.New(), "public", desired, schemigo.ApplyOptions{BackupDir: backup})
	var applyErr *schemigo.ApplyError
	if !errors.As(err, &applyErr) || applyErr.BackupPath == "" {
		t.Fatalf("want preserved snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(applyErr.BackupPath, "main.go")); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := schemigo.Load(t.Context(), applyErr.BackupPath); err != nil || snapshot.Name != "public" || len(snapshot.Tables) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	var tableExists bool
	if err := db.QueryRowContext(t.Context(), `SELECT pg_catalog.to_regclass('public.users') IS NOT NULL`).Scan(&tableExists); err != nil || tableExists {
		t.Fatalf("second-statement failure left table=%v err=%v", tableExists, err)
	}
	if err := schemigo.Apply(t.Context(), db, postgres.New(), "public", tableSchema(simpleTable("users")), schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatalf("retry after failed migration: %v", err)
	}
}

func TestApplyEnumValueBeforeTableDefault(t *testing.T) {
	db := testdb.Postgres(t)
	base := schemigo.Schema{Name: "public", Enums: []schemigo.Enum{{Name: "status", Values: []string{"pending"}}}}
	if err := schemigo.Apply(t.Context(), db, postgres.New(), "public", base, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	desired := schemigo.Schema{Name: "public", Enums: []schemigo.Enum{{Name: "status", Values: []string{"pending", "shipped"}}}, Tables: []schemigo.Table{{Name: "orders", Columns: []schemigo.Column{{Name: "state", Type: "status", Default: "'shipped'"}}}}}
	if err := schemigo.Apply(t.Context(), db, postgres.New(), "public", desired, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO public.orders DEFAULT VALUES`); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRowContext(t.Context(), `SELECT state::text FROM public.orders`).Scan(&state); err != nil || state != "shipped" {
		t.Fatalf("state=%q err=%v", state, err)
	}
}

func TestPostgresConcurrentApplyCancellation(t *testing.T) {
	db := testdb.Postgres(t)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	unlock, err := postgres.New().Lock(t.Context(), conn, "public")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(context.Background())
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	err = schemigo.Apply(ctx, db, postgres.New(), "public", schemigo.Schema{Name: "public"}, schemigo.ApplyOptions{NoBackup: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for lock: %v", err)
	}
}
