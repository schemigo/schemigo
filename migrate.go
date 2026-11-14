package schemigo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ApplyOptions controls whether Apply writes a schema snapshot and where it lives.
type ApplyOptions struct {
	NoBackup  bool
	BackupDir string
	// ExpectedHash, when set, requires the locked plan to match the SQL preview.
	ExpectedHash string
}

// ApplyError reports an unsuccessful migration and the preserved schema snapshot.
// A snapshot describes database objects; it does not restore table data.
type ApplyError struct {
	BackupPath string
	// CompletedStatements counts DDL statements committed before the failure.
	CompletedStatements int
	Err                 error
}

func (e *ApplyError) Error() string {
	detail := fmt.Sprintf("apply migration: %v", e.Err)
	if e.CompletedStatements > 0 {
		word := "statements"
		if e.CompletedStatements == 1 {
			word = "statement"
		}
		detail += fmt.Sprintf("; %d %s completed", e.CompletedStatements, word)
	}
	if e.BackupPath == "" {
		return detail
	}
	return fmt.Sprintf("%s; schema snapshot: %s; restore with schemigo -d <dsn> -dir %q", detail, e.BackupPath, e.BackupPath)
}
func (e *ApplyError) Unwrap() error { return e.Err }

// PlanMigration inspects the current namespace and builds a plan without applying SQL.
func PlanMigration(ctx context.Context, db *sql.DB, driver Driver, schemaName string, desired Schema) (Plan, error) {
	if db == nil || driver == nil {
		return Plan{}, fmt.Errorf("plan migration: database and driver are required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("plan migration: acquire connection: %w", err)
	}
	defer conn.Close()
	current, err := driver.Inspect(ctx, conn, schemaName)
	if err != nil {
		return Plan{}, fmt.Errorf("plan migration: inspect: %w", err)
	}
	plan, err := driver.Diff(ctx, conn, current, desired)
	if err != nil {
		return Plan{}, fmt.Errorf("plan migration: diff: %w", err)
	}
	return plan, nil
}

// History reads recorded migrations without creating a history table.
func History(ctx context.Context, db *sql.DB, driver Driver, schemaName string) ([]Migration, error) {
	if db == nil || driver == nil {
		return nil, fmt.Errorf("migration history: database and driver are required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration history: acquire connection: %w", err)
	}
	defer conn.Close()
	return driver.ListMigrations(ctx, conn, schemaName)
}

// Apply serializes, validates and applies a fresh diff under one connection.
// A nonempty plan is recorded even when its hash appeared earlier, because drift
// may require replay. Ordinary statements run in transactions; OutsideTransaction
// statements commit before later commands can use their effects.
func Apply(ctx context.Context, db *sql.DB, driver Driver, schemaName string, desired Schema, options ApplyOptions) (err error) {
	if db == nil || driver == nil {
		return &ApplyError{Err: fmt.Errorf("database and driver are required")}
	}
	conn, e := db.Conn(ctx)
	if e != nil {
		return &ApplyError{Err: fmt.Errorf("acquire connection: %w", e)}
	}
	backupPath := ""
	completed := 0
	defer func() {
		if err == nil && backupPath != "" {
			if e := os.RemoveAll(backupPath); e != nil {
				err = &ApplyError{BackupPath: backupPath, CompletedStatements: completed, Err: fmt.Errorf("remove snapshot: %w", e)}
			}
		}
	}()
	defer func() {
		if e := conn.Close(); e != nil {
			err = errors.Join(err, &ApplyError{BackupPath: backupPath, CompletedStatements: completed, Err: fmt.Errorf("close connection: %w", e)})
		}
	}()
	unlock, e := driver.Lock(ctx, conn, schemaName)
	if e != nil {
		return &ApplyError{Err: fmt.Errorf("lock: %w", e)}
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := unlock(unlockCtx); e != nil {
			err = errors.Join(err, &ApplyError{BackupPath: backupPath, CompletedStatements: completed, Err: fmt.Errorf("unlock: %w", e)})
		}
	}()
	current, e := driver.Inspect(ctx, conn, schemaName)
	if e != nil {
		return &ApplyError{Err: fmt.Errorf("inspect: %w", e)}
	}
	plan, e := driver.Diff(ctx, conn, current, desired)
	if e != nil {
		return &ApplyError{Err: fmt.Errorf("diff: %w", e)}
	}
	if options.ExpectedHash != "" && plan.SHA256() != options.ExpectedHash {
		return &ApplyError{Err: fmt.Errorf("plan changed after preview; review the SQL again")}
	}
	if len(plan.Statements) == 0 {
		return nil
	}
	for i, statement := range plan.Statements {
		if e := driver.Validate(ctx, conn, statement); e != nil {
			return &ApplyError{Err: fmt.Errorf("validate statement %d: %w", i+1, e)}
		}
	}
	if !options.NoBackup {
		backupRoot := options.BackupDir
		if backupRoot == "" {
			backupRoot = "schemigo_backup"
		}
		if _, e = schemaLocation(ctx, filepath.Join(backupRoot, "snapshot")); e != nil {
			return &ApplyError{Err: fmt.Errorf("snapshot location: %w", e)}
		}
		if e = os.MkdirAll(backupRoot, 0755); e != nil {
			return &ApplyError{Err: fmt.Errorf("snapshot directory: %w", e)}
		}
		backupPath, e = os.MkdirTemp(backupRoot, "snapshot-")
		if e != nil {
			return &ApplyError{Err: fmt.Errorf("snapshot directory: %w", e)}
		}
		if e = publishSchema(ctx, current, backupPath, false); e != nil {
			return &ApplyError{BackupPath: backupPath, Err: fmt.Errorf("snapshot: %w", e)}
		}
	}
	fail := func(e error) error {
		return &ApplyError{BackupPath: backupPath, CompletedStatements: completed, Err: e}
	}
	if e = driver.EnsureHistory(ctx, conn, schemaName); e != nil {
		return fail(fmt.Errorf("ensure history: %w", e))
	}
	var tx *sql.Tx
	for i, statement := range plan.Statements {
		if statement.OutsideTransaction {
			if tx != nil {
				if e = tx.Commit(); e != nil {
					return fail(fmt.Errorf("commit before statement %d: %w", i+1, e))
				}
				completed = i
				tx = nil
			}
			_, e = conn.ExecContext(ctx, statement.SQL)
		} else {
			if tx == nil {
				tx, e = conn.BeginTx(ctx, nil)
				if e != nil {
					return fail(fmt.Errorf("begin statement %d: %w", i+1, e))
				}
			}
			_, e = tx.ExecContext(ctx, statement.SQL)
		}
		if e != nil {
			if tx != nil {
				_ = tx.Rollback()
			}
			return fail(fmt.Errorf("execute statement %d: %w", i+1, e))
		}
		if statement.OutsideTransaction {
			completed = i + 1
		}
	}
	migration := Migration{Hash: plan.SHA256()}
	recorded := false
	if tx != nil {
		if recorder, ok := driver.(interface {
			RecordMigrationTx(context.Context, *sql.Tx, string, Migration) error
		}); ok {
			if e := recorder.RecordMigrationTx(ctx, tx, schemaName, migration); e != nil {
				_ = tx.Rollback()
				return fail(fmt.Errorf("record migration: %w", e))
			}
			recorded = true
		}
		if e = tx.Commit(); e != nil {
			return fail(fmt.Errorf("commit migration: %w", e))
		}
		completed = len(plan.Statements)
	}
	if !recorded {
		if e = driver.RecordMigration(ctx, conn, schemaName, migration); e != nil {
			return fail(fmt.Errorf("record migration: %w", e))
		}
	}
	return nil
}
