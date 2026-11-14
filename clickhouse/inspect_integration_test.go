//go:build integration

package clickhouse_test

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/clickhouse"
	"github.com/schemigo/schemigo/internal/testdb"
)

func TestClickHouseInspect(t *testing.T) {
	db, _ := testdb.ClickHouseURL(t)
	for _, sql := range []string{
		"CREATE TABLE default.events (id UInt64, at Date, body String, INDEX body_idx body TYPE bloom_filter GRANULARITY 1, CONSTRAINT positive CHECK id > 0) ENGINE = MergeTree PARTITION BY toYYYYMM(at) ORDER BY id",
		"CREATE MATERIALIZED VIEW default.mv ENGINE = MergeTree ORDER BY id AS SELECT id FROM default.events",
		"CREATE FUNCTION schemigo_twice AS (x) -> x * 2",
	} {
		if _, err := db.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	d := clickhouse.New(nil)
	s, err := d.Inspect(t.Context(), conn, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tables) != 1 || !strings.HasPrefix(s.Tables[0].Engine, "MergeTree") || s.Tables[0].OrderBy != "id" || s.Tables[0].PartitionBy != "toYYYYMM(at)" || len(s.Tables[0].Indexes) != 1 || len(s.Tables[0].Checks) != 1 || len(s.Views) != 1 || !s.Views[0].Materialized || len(s.Functions) != 1 {
		t.Fatalf("inspection lost objects: %+v", s)
	}
	p, err := d.Diff(t.Context(), conn, s, s)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("self diff: %s %v", p.SQL(), err)
	}
	desired := schemigo.Schema{Name: "default", Tables: []schemigo.Table{{Name: "events", Engine: "MergeTree", PartitionBy: "toYYYYMM(at)", OrderBy: "id", Columns: []schemigo.Column{{Name: "id", Type: clickhouse.UInt64}, {Name: "at", Type: clickhouse.Date}, {Name: "body", Type: clickhouse.String}}, Indexes: []schemigo.Index{{Name: "body_idx", Expression: "body", Type: "bloom_filter", Granularity: 1}}, Checks: []schemigo.Check{{Name: "positive", Expression: "id > 0"}}}}, Views: s.Views, Functions: []schemigo.Function{{Name: "schemigo_twice", Parameters: "x", Body: "x * 2"}}}
	p, err = d.Diff(t.Context(), conn, s, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("round trip: %s %v", p.SQL(), err)
	}
}
