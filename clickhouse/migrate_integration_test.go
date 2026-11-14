//go:build integration

package clickhouse_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/clickhouse"
	"github.com/schemigo/schemigo/internal/testdb"
)

func migrationSchema() schemigo.Schema {
	return schemigo.Schema{Name: "default", Tables: []schemigo.Table{{Name: "events", Engine: "MergeTree", OrderBy: "id", Columns: []schemigo.Column{{Name: "id", Type: clickhouse.UInt64}, {Name: "body", Type: clickhouse.String}}}}}
}
func migrationDriver(t *testing.T) (*sql.DB, schemigo.Driver, clickhouse.Locker) {
	t.Helper()
	db, _ := testdb.ClickHouseURL(t)
	l, err := clickhouse.NewKeeperLocker(t.Context(), []string{testdb.Keeper(t)}, "/schemigo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	return db, clickhouse.New(l), l
}
func TestClickHouseHistorySkipAndRepeatedHashAfterDrift(t *testing.T) {
	db, d, _ := migrationDriver(t)
	s := migrationSchema()
	options := schemigo.ApplyOptions{NoBackup: true}
	for i := 0; i < 2; i++ {
		if err := schemigo.Apply(t.Context(), db, d, s.Name, s, options); err != nil {
			t.Fatal(err)
		}
	}
	h, err := schemigo.History(t.Context(), db, d, s.Name)
	if err != nil || len(h) != 1 {
		t.Fatalf("history=%+v %v", h, err)
	}
	if _, err := db.ExecContext(t.Context(), "DROP TABLE default.events"); err != nil {
		t.Fatal(err)
	}
	if err := schemigo.Apply(t.Context(), db, d, s.Name, s, options); err != nil {
		t.Fatal(err)
	}
	h, err = schemigo.History(t.Context(), db, d, s.Name)
	if err != nil || len(h) != 2 || h[0].Hash != h[1].Hash {
		t.Fatalf("drift history=%+v %v", h, err)
	}
}
func TestClickHouseRetryAfterPartialDDL(t *testing.T) {
	db, d, _ := migrationDriver(t)
	s := migrationSchema()
	s.Tables = append(s.Tables, schemigo.Table{Name: "broken", Engine: "NoSuchEngine", Columns: []schemigo.Column{{Name: "id", Type: clickhouse.UInt64}}})
	if err := schemigo.Apply(t.Context(), db, d, s.Name, s, schemigo.ApplyOptions{NoBackup: true}); err == nil {
		t.Fatal("bad engine accepted")
	}
	h, err := schemigo.History(t.Context(), db, d, s.Name)
	if err != nil || len(h) != 0 {
		t.Fatalf("failed migration recorded: %+v %v", h, err)
	}
	s.Tables[1].Engine = "MergeTree"
	s.Tables[1].OrderBy = "tuple()"
	if err := schemigo.Apply(t.Context(), db, d, s.Name, s, schemigo.ApplyOptions{NoBackup: true}); err != nil {
		t.Fatal(err)
	}
	p, err := schemigo.PlanMigration(t.Context(), db, d, s.Name, s)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("retry diff=%s %v", p.SQL(), err)
	}
}
func TestClickHouseApplyWithoutKeeperFailsBeforeDDL(t *testing.T) {
	db, _ := testdb.ClickHouseURL(t)
	err := schemigo.Apply(t.Context(), db, clickhouse.New(nil), "default", migrationSchema(), schemigo.ApplyOptions{NoBackup: true})
	if err == nil || !strings.Contains(err.Error(), "Keeper") {
		t.Fatalf("%v", err)
	}
	var n uint64
	if err := db.QueryRowContext(t.Context(), "SELECT count() FROM system.tables WHERE database = 'default'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("DDL happened: %d %v", n, err)
	}
}

type closeSessionDriver struct {
	schemigo.Driver
	locker clickhouse.Locker
}

func (d closeSessionDriver) EnsureHistory(ctx context.Context, c *sql.Conn, s string) error {
	if err := d.Driver.EnsureHistory(ctx, c, s); err != nil {
		return err
	}
	return d.locker.Close()
}
func (d closeSessionDriver) MigrationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return d.Driver.(interface {
		MigrationContext(context.Context) (context.Context, context.CancelFunc)
	}).MigrationContext(ctx)
}
func TestClickHouseLockSessionLost(t *testing.T) {
	db, d, l := migrationDriver(t)
	err := schemigo.Apply(t.Context(), db, closeSessionDriver{d, l}, "default", migrationSchema(), schemigo.ApplyOptions{NoBackup: true})
	if err == nil {
		t.Fatal("migration continued after session loss")
	}
	var n uint64
	if err := db.QueryRowContext(t.Context(), "SELECT count() FROM system.tables WHERE database = 'default' AND name = 'events'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("later DDL ran: %d %v", n, err)
	}
}

func TestClickHouseConcurrentApply(t *testing.T) {
	db, dsn := testdb.ClickHouseURL(t)
	endpoint := testdb.Keeper(t)
	otherDB, err := clickhouse.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	owner, err := clickhouse.NewKeeperLocker(t.Context(), []string{endpoint}, "/schemigo")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	other, err := clickhouse.NewKeeperLocker(t.Context(), []string{endpoint}, "/schemigo")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s := migrationSchema()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, client := range []struct {
		db     *sql.DB
		locker clickhouse.Locker
	}{{db, owner}, {otherDB, other}} {
		go func() {
			<-start
			results <- schemigo.Apply(t.Context(), client.db, clickhouse.New(client.locker), s.Name, s, schemigo.ApplyOptions{NoBackup: true})
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	h, err := schemigo.History(t.Context(), db, clickhouse.New(nil), s.Name)
	if err != nil || len(h) != 1 {
		t.Fatalf("concurrent applies recorded duplicate history: %+v %v", h, err)
	}
}
