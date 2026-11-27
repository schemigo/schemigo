package main

import (
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/postgres"
)

func TestMigrationPromptColors(t *testing.T) {
	t.Parallel()
	const choices = "Apply migration? (\x1b[32my\x1b[0m = apply, \x1b[31mn\x1b[0m = cancel"
	if got, want := migrationPrompt(true, true), choices+", \x1b[35mc\x1b[0m = check): "; got != want {
		t.Errorf("check prompt = %q, want %q", got, want)
	}
	if got, want := migrationPrompt(false, true), choices+"): "; got != want {
		t.Errorf("apply prompt = %q, want %q", got, want)
	}
}

func TestPrintSQLToWriterPreservesPlan(t *testing.T) {
	t.Parallel()
	plan := schemigo.Plan{Statements: []schemigo.Statement{
		{SQL: "CREATE TABLE items (id INT, name TEXT)"},
		{SQL: "ALTER TABLE items ADD COLUMN price INT;"},
	}}
	var out strings.Builder
	if err := printSQL(&out, plan, "postgres"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != plan.SQL() {
		t.Errorf("got %q, want %q", got, plan.SQL())
	}
}

func TestPrintNoChangesToWriter(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := printNoChanges(&out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "No changes.\n" {
		t.Errorf("got %q, want %q", got, "No changes.\n")
	}
}

func TestFormatBoxWrapsMultilineMessage(t *testing.T) {
	t.Parallel()
	const want = "╭──────────────╮\n" +
		"│ load schema: │\n" +
		"│ invalid      │\n" +
		"│ more         │\n" +
		"╰──────────────╯"
	if got := formatBox("load schema: invalid\r\nmore", 12); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatStatement(t *testing.T) {
	t.Parallel()
	t.Run("table layout preserves expressions and strings", func(t *testing.T) {
		const sql = "CREATE TABLE items (payload TEXT DEFAULT 'a, DROP; b', ids INT[] DEFAULT ARRAY[1,2], " +
			"price DECIMAL(10, 2) /* comma, /* nested */ ; */)"
		const want = "CREATE TABLE items (\n  payload TEXT DEFAULT 'a, DROP; b',\n" +
			"  ids INT[] DEFAULT ARRAY[1,2],\n  price DECIMAL(10, 2) /* comma, /* nested */ ; */\n)"
		if got := formatStatement(sql, "postgres", false); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("postgres guard highlights inner commands", func(t *testing.T) {
		body := `BEGIN IF EXISTS (SELECT 1) THEN DROP TABLE "DROP"; RAISE NOTICE 'C:\tmp; CREATE'; END IF; END`
		sql := "DO " + postgres.QuoteLiteral(body)
		const want = `DO $$
BEGIN
  IF EXISTS (
    SELECT 1
  ) THEN
    DROP TABLE "DROP";
    RAISE NOTICE 'C:\tmp; CREATE';
  END IF;
END
$$`
		if got := formatStatement(sql, "postgres", false); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		got := formatStatement(sql, "postgres", true)
		if !strings.Contains(got, "\x1b[31mDROP\x1b[0m") || strings.Contains(got, "\"\x1b[31m") {
			t.Errorf("expected only the command DROP to be red, got %q", got)
		}
	})

	t.Run("nested postgres guard formats and highlights generated DDL", func(t *testing.T) {
		const inner = "BEGIN\nIF EXISTS (SELECT FROM catalog WHERE id=1 AND active) THEN " +
			"RAISE EXCEPTION 'already exists'; ELSE CREATE TABLE items (id bigint, name text); END IF; END"
		outer := "DECLARE previous_path text := current_setting('search_path'); BEGIN DO " +
			postgres.QuoteLiteral(inner) + "; PERFORM set_config('search_path', previous_path, true); END"
		sql := "DO " + postgres.QuoteLiteral(outer)
		const want = `DO $$
DECLARE
  previous_path text := current_setting('search_path');
BEGIN
  DO $body$
  BEGIN
    IF EXISTS (
      SELECT
      FROM catalog
      WHERE id=1
        AND active
    ) THEN
      RAISE EXCEPTION 'already exists';
    ELSE
      CREATE TABLE items (
        id bigint,
        name text
      );
    END IF;
  END
  $body$;
  PERFORM set_config('search_path', previous_path, true);
END
$$`
		if got := formatStatement(sql, "postgres", false); got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
		colored := formatStatement(sql, "postgres", true)
		for _, token := range []string{"\x1b[32mCREATE\x1b[0m", "\x1b[36mSELECT\x1b[0m", "\x1b[36mRAISE\x1b[0m"} {
			if !strings.Contains(colored, token) {
				t.Errorf("missing highlight %q in %q", token, colored)
			}
		}
	})

	t.Run("mysql batch preserves semicolons inside literals", func(t *testing.T) {
		const sql = "SET @ddl = 'ALTER TABLE `items` ADD COLUMN `a;b` INT'; PREPARE stmt FROM @ddl; EXECUTE stmt"
		const want = "SET @ddl = 'ALTER TABLE `items` ADD COLUMN `a;b` INT';\nPREPARE stmt FROM @ddl;\nEXECUTE stmt"
		if got := formatStatement(sql, "mysql", false); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("dollar-quoted function body stays intact", func(t *testing.T) {
		const sql = "CREATE FUNCTION f() RETURNS void AS $body$\nBEGIN\n  DROP TABLE items;\nEND\n$body$ LANGUAGE plpgsql"
		got := formatStatement(sql, "postgres", true)
		if !strings.Contains(got, "$body$\nBEGIN\n  DROP TABLE items;\nEND\n$body$") {
			t.Errorf("function body changed in %q", got)
		}
	})
}
