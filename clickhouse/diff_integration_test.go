//go:build integration

package clickhouse_test

import (
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/clickhouse"
)

func TestClickHouseTableAndMaterializedDiff(t *testing.T) {
	db, d, _ := migrationDriver(t)
	s := migrationSchema()
	s.Views = []schemigo.View{{Name: "mv", Materialized: true, Definition: "ENGINE = MergeTree ORDER BY id AS SELECT id FROM default.events"}}
	apply := func() {
		t.Helper()
		if err := schemigo.Apply(t.Context(), db, d, s.Name, s, schemigo.ApplyOptions{NoBackup: true}); err != nil {
			t.Fatal(err)
		}
		p, err := schemigo.PlanMigration(t.Context(), db, d, s.Name, s)
		if err != nil || len(p.Statements) != 0 {
			t.Fatalf("repeat diff: %s %v", p.SQL(), err)
		}
	}
	apply()
	if _, err := db.ExecContext(t.Context(), "INSERT INTO default.events VALUES (1, 'keep')"); err != nil {
		t.Fatal(err)
	}
	s.Tables[0].Columns = append(s.Tables[0].Columns, schemigo.Column{Name: "count", Type: clickhouse.UInt32, Default: "0"})
	s.Tables[0].Indexes = []schemigo.Index{{Name: "body_idx", Columns: []string{"body"}, Type: "bloom_filter", Granularity: 1}}
	s.Tables[0].Checks = []schemigo.Check{{Name: "positive", Expression: "id > 0"}}
	s.Views[0].Definition = "ENGINE = MergeTree ORDER BY id AS SELECT id FROM default.events WHERE id > 0"
	apply()
	s.Tables[0].Columns[2].Type = clickhouse.UInt64
	s.Tables[0].Columns[2].Default = ""
	s.Tables[0].Indexes = nil
	s.Tables[0].Checks = nil
	apply()
	var body string
	if err := db.QueryRowContext(t.Context(), "SELECT body FROM default.events WHERE id=1").Scan(&body); err != nil || body != "keep" {
		t.Fatalf("data=%q %v", body, err)
	}
}
