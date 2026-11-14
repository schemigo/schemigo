package mysql_test

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/mysql"
)

func TestDiffCreatesMySQLTable(t *testing.T) {
	current := schemigo.Schema{Name: "app"}
	desired := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{
		Name: "order",
		Columns: []schemigo.Column{
			{Name: "id", Type: "bigint", NotNull: true, PrimaryKey: true},
			{Name: "status", Type: "varchar(32)", Default: "'pending'"},
		},
		Indexes: []schemigo.Index{{Name: "idx_status", Columns: []string{"status"}, Unique: true}},
		Checks:  []schemigo.Check{{Name: "chk_status", Expression: "status <> ''"}},
	}}}
	plan, err := mysql.New().Diff(t.Context(), nil, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	for _, want := range []string{"CREATE TABLE IF NOT EXISTS `app`.`order`", "`id` bigint NOT NULL", "PRIMARY KEY (`id`)", "UNIQUE KEY `idx_status` (`status`)", "CONSTRAINT `chk_status` CHECK (status <> '')"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing %q in %s", want, sql)
		}
	}
	for _, statement := range plan.Statements {
		if !statement.OutsideTransaction || !statement.SkipExplain {
			t.Fatalf("MySQL DDL must run without a transaction or EXPLAIN: %+v", statement)
		}
	}
}

func TestDiffChangesMySQLTable(t *testing.T) {
	current := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}, {Name: "old_name", Type: "varchar(32)"}}}}}
	desired := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}, {Name: "name", Type: "varchar(32)"}, {Name: "active", Type: "boolean", NotNull: true, Default: "1"}}, Indexes: []schemigo.Index{{Name: "idx_name", Columns: []string{"name"}}}}}}
	plan, err := mysql.New().Diff(t.Context(), nil, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	for _, want := range []string{"RENAME COLUMN `old_name` TO `name`", "ADD COLUMN `active` boolean NOT NULL DEFAULT 1", "ADD KEY `idx_name` (`name`)"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing %q in %s", want, sql)
		}
	}
	if strings.Contains(sql, "DROP COLUMN `old_name`") {
		t.Fatalf("rename became destructive: %s", sql)
	}
	if len(plan.Statements) == 0 {
		t.Fatal("expected a migration")
	}
	for _, statement := range plan.Statements {
		if !statement.OutsideTransaction {
			t.Fatalf("DDL must autocommit: %+v", statement)
		}
	}
}

func TestDiffMySQLNoop(t *testing.T) {
	schema := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}}}}}
	plan, err := mysql.New().Diff(t.Context(), nil, schema, schema)
	if err != nil || len(plan.Statements) != 0 {
		t.Fatalf("unchanged schema produced a plan: %+v, %v", plan, err)
	}
}

func TestDiffMySQLTypeAliasesDoNotCreateDrift(t *testing.T) {
	current := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}, {Name: "active", Type: "tinyint(1)"}}}}}
	desired := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "int8"}, {Name: "active", Type: mysql.Boolean}}}}}
	plan, err := mysql.New().Diff(t.Context(), nil, current, desired)
	if err != nil || len(plan.Statements) != 0 {
		t.Fatalf("MySQL type aliases created drift: %+v, %v", plan, err)
	}
}

func TestDiffMySQLTypeAliasesDoNotRecreateTrigger(t *testing.T) {
	current := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "active", Type: "tinyint(1)"}}}}, Functions: []schemigo.Function{{Name: "audit_user", Body: "BEGIN SET @audit_seen = 1; END"}}, Triggers: []schemigo.Trigger{{Name: "users_audit", Table: "users", When: "AFTER", Event: "INSERT", Function: "audit_user"}}}
	desired := current
	desired.Tables = []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "active", Type: mysql.Boolean}}}}
	plan, err := mysql.New().Diff(t.Context(), nil, current, desired)
	if err != nil || len(plan.Statements) != 0 {
		t.Fatalf("type alias recreated trigger: %+v, %v", plan, err)
	}
}

func TestDiffRejectsUnsupportedMySQLObjects(t *testing.T) {
	base := schemigo.Schema{Name: "app"}
	for _, test := range []struct {
		name    string
		desired schemigo.Schema
	}{
		{"extension", schemigo.Schema{Name: "app", Extensions: []schemigo.Extension{{Name: "pg_trgm"}}}},
		{"enum", schemigo.Schema{Name: "app", Enums: []schemigo.Enum{{Name: "status", Values: []string{"new"}}}}},
		{"materialized view", schemigo.Schema{Name: "app", Views: []schemigo.View{{Name: "v", Definition: "SELECT 1", Materialized: true}}}},
		{"partial index", schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}}, Indexes: []schemigo.Index{{Name: "idx", Columns: []string{"id"}, Where: "id > 0"}}}}}},
		{"routine overload", schemigo.Schema{Name: "app", Functions: []schemigo.Function{{Name: "f", Parameters: "x int", Returns: "int", Body: "RETURN x"}, {Name: "f", Parameters: "x bigint", Returns: "bigint", Body: "RETURN x"}}}},
		{"migration history table", schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "schema_migrations", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := mysql.New().Diff(t.Context(), nil, base, test.desired); err == nil {
				t.Fatal("unsupported object was accepted")
			}
		})
	}
}

func TestDiffMySQLViewAndRoutine(t *testing.T) {
	current := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}}}}}
	desired := current
	desired.Functions = []schemigo.Function{{Name: "audit_user", Body: "BEGIN SET @audit_seen = 1; END"}}
	desired.Triggers = []schemigo.Trigger{{Name: "users_audit", Table: "users", When: "AFTER", Event: "INSERT", Function: "audit_user"}}
	desired.Views = []schemigo.View{{Name: "user_ids", Definition: "SELECT id FROM users"}}
	plan, err := mysql.New().Diff(t.Context(), nil, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	for _, want := range []string{"CREATE PROCEDURE `app`.`audit_user`", "CREATE TRIGGER `app`.`users_audit`", "CALL `app`.`audit_user`()", "CREATE OR REPLACE VIEW `app`.`user_ids` AS SELECT id FROM users"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing %q in %s", want, sql)
		}
	}
}

func TestDiffDropsPrimaryKeyBeforeMakingColumnNullable(t *testing.T) {
	current := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint", NotNull: true, PrimaryKey: true}}}}}
	desired := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}}}}}
	plan, err := mysql.New().Diff(t.Context(), nil, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	drop := strings.Index(sql, "DROP PRIMARY KEY")
	modify := strings.Index(sql, "MODIFY COLUMN `id` bigint")
	if drop < 0 || modify < 0 || drop > modify {
		t.Fatalf("primary key must be dropped before nullable column change: %s", sql)
	}
}

func TestDiffDropsForeignKeysBeforeTables(t *testing.T) {
	current := schemigo.Schema{Name: "app", Tables: []schemigo.Table{
		{Name: "parents", Columns: []schemigo.Column{{Name: "id", Type: "bigint", PrimaryKey: true}}},
		{Name: "children", Columns: []schemigo.Column{{Name: "id", Type: "bigint"}, {Name: "parent_id", Type: "bigint"}}, ForeignKeys: []schemigo.ForeignKey{{Name: "fk_parent", Columns: []string{"parent_id"}, ReferencedTable: "parents", ReferencedColumns: []string{"id"}}}},
	}}
	plan, err := mysql.New().Diff(t.Context(), nil, current, schemigo.Schema{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	fk := strings.Index(sql, "DROP FOREIGN KEY `fk_parent`")
	table := strings.Index(sql, "DROP TABLE IF EXISTS `app`.`parents`")
	if fk < 0 || table < 0 || fk > table {
		t.Fatalf("foreign key must be removed before parent table: %s", sql)
	}
}

func TestDiffDropsChangedViewBeforeColumnRename(t *testing.T) {
	current := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "old_name", Type: "varchar(32)"}}}}, Views: []schemigo.View{{Name: "names", Definition: "SELECT old_name FROM users"}}}
	desired := schemigo.Schema{Name: "app", Tables: []schemigo.Table{{Name: "users", Columns: []schemigo.Column{{Name: "new_name", Type: "varchar(32)"}}}}, Views: []schemigo.View{{Name: "names", Definition: "SELECT new_name FROM users"}}}
	plan, err := mysql.New().Diff(t.Context(), nil, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	drop := strings.Index(sql, "DROP VIEW IF EXISTS `app`.`names`")
	rename := strings.Index(sql, "RENAME COLUMN `old_name` TO `new_name`")
	create := strings.Index(sql, "CREATE OR REPLACE VIEW `app`.`names`")
	if drop < 0 || rename < 0 || create < 0 || drop > rename || rename > create {
		t.Fatalf("view dependency order is unsafe: %s", sql)
	}
}
