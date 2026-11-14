package clickhouse

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
)

func TestViewDefinition(t *testing.T) {
	for _, tc := range []struct {
		name, sql, want string
		materialized    bool
	}{
		{"ordinary", "CREATE VIEW db.v (`x` UInt8) AS SELECT ' AS x ' AS x", "SELECT ' AS x ' AS x", false},
		{"materialized", "CREATE MATERIALIZED VIEW db.mv TO db.target AS SELECT 1 AS x", "TO db.target AS SELECT 1 AS x", true},
		{"storage", "CREATE MATERIALIZED VIEW `db`.`mv` (`id` UInt64) ENGINE = MergeTree ORDER BY id AS SELECT id FROM db.events", "(`id` UInt64) ENGINE = MergeTree ORDER BY id AS SELECT id FROM db.events", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := viewDefinition(tc.sql, tc.materialized)
			if err != nil || got != tc.want {
				t.Fatalf("%q %v", got, err)
			}
		})
	}
}
func TestFunctionDefinition(t *testing.T) {
	f, err := functionDefinition("plus_one", "CREATE FUNCTION plus_one AS (x) -> (x + 1)")
	if err != nil || f.Parameters != "x" || f.Body != "(x + 1)" {
		t.Fatalf("%+v %v", f, err)
	}
}
func TestCanonicalPreservesLiterals(t *testing.T) {
	if canonical("`x` + 1") != canonical("x+1") {
		t.Fatal("formatting differs")
	}
	if canonical("'a b'") == canonical("'ab'") || canonical("'A'") == canonical("'a'") {
		t.Fatal("literal changed")
	}
	if QuoteIdent("a`b\\c") != "`a\\`b\\\\c`" {
		t.Fatal("identifier escaping")
	}
}

func TestTableEngine(t *testing.T) {
	got, err := tableEngine("ReplicatedMergeTree('/path', 'replica') PARTITION BY toYYYYMM(at) PRIMARY KEY id ORDER BY (id, at)")
	if err != nil || got != "ReplicatedMergeTree('/path', 'replica')" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := tableEngine("MergeTree ORDER BY id TTL at + INTERVAL 1 DAY"); err == nil {
		t.Fatal("unrepresentable TTL silently dropped")
	}
	if keyExpression("id, at") != "(id, at)" || keyExpression("tuple(id, at)") != "tuple(id, at)" {
		t.Fatal("multi-column key changed")
	}
}
func TestTableChecks(t *testing.T) {
	checks, err := tableChecks("CREATE TABLE db.t (id UInt64, CONSTRAINT `positive` CHECK id > 0, CONSTRAINT `literal` CHECK body != 'a,b)') ENGINE = MergeTree ORDER BY id")
	if err != nil || len(checks) != 2 || checks[0].Name != "positive" || checks[0].Expression != "id > 0" || checks[1].Expression != "body != 'a,b)'" {
		t.Fatalf("%+v %v", checks, err)
	}
	if _, err := tableChecks("CREATE TABLE db.t (id UInt64, CONSTRAINT x ASSUME id > 0) ENGINE = MergeTree ORDER BY id"); err == nil {
		t.Fatal("ASSUME silently converted into CHECK")
	}
}
func TestMaterializedInferredColumns(t *testing.T) {
	a := schemigo.View{Materialized: true, Definition: "(`id` UInt64) ENGINE = MergeTree ORDER BY id SETTINGS index_granularity = 8192 AS SELECT id FROM events"}
	b := schemigo.View{Materialized: true, Definition: "ENGINE = MergeTree ORDER BY id AS SELECT id FROM events"}
	if !sameView(a, b) {
		t.Fatal("inferred columns leave a diff")
	}
	b.Definition = "(`id` UInt32) ENGINE = MergeTree ORDER BY id AS SELECT id FROM events"
	if sameView(a, b) {
		t.Fatal("explicit column type change ignored")
	}
}

func TestViewSecurityPreserved(t *testing.T) {
	current := schemigo.Schema{Name: "db", Views: []schemigo.View{{Name: "mv", Materialized: true, Definition: "(`id` UInt64) ENGINE = MergeTree ORDER BY id DEFINER = alice SQL SECURITY DEFINER AS SELECT 1 AS id"}}}
	desired := schemigo.Schema{Name: "db", Views: []schemigo.View{{Name: "mv", Materialized: true, Definition: "ENGINE = MergeTree ORDER BY id AS SELECT 1 AS id"}}}
	p, err := New(nil).Diff(t.Context(), nil, current, desired)
	if err != nil || len(p.Statements) != 0 {
		t.Fatalf("implicit security diff: %s %v", p.SQL(), err)
	}
	desired.Views[0].Definition = "ENGINE = MergeTree ORDER BY id AS SELECT 2 AS id"
	p, err = New(nil).Diff(t.Context(), nil, current, desired)
	if err != nil || !strings.Contains(p.SQL(), "DEFINER = alice SQL SECURITY DEFINER AS SELECT 2") {
		t.Fatalf("security lost on recreate: %s %v", p.SQL(), err)
	}
	got, err := viewDefinition("CREATE VIEW db.v (id UInt64) DEFINER = alice SQL SECURITY DEFINER AS SELECT id FROM db.events", false)
	if err != nil || got != "DEFINER = alice SQL SECURITY DEFINER AS SELECT id FROM db.events" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestMergeTreeSettingsRoundTrip(t *testing.T) {
	engine, err := tableEngine("MergeTree ORDER BY id SETTINGS index_granularity = 8192")
	if err != nil || engine != "MergeTree SETTINGS index_granularity = 8192" {
		t.Fatalf("%q %v", engine, err)
	}
	if engineKey(engine) != engineKey("MergeTree") {
		t.Fatal("implicit engine setting leaves a diff")
	}
	table := schemigo.Table{Name: "t", Engine: engine, OrderBy: "id", Columns: []schemigo.Column{{Name: "id", Type: UInt64}}}
	sql := createTable("db", table)
	if !strings.HasSuffix(sql, "ORDER BY id SETTINGS index_granularity = 8192") {
		t.Fatal(sql)
	}
}
