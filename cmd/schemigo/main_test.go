package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/schemigo/schemigo"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestPrintSQLReportsWriteError(t *testing.T) {
	plan := schemigo.Plan{Statements: []schemigo.Statement{{SQL: "SELECT 1"}}}
	if err := printSQL(failingWriter{}, plan, "postgres"); err == nil {
		t.Fatal("SQL output error was ignored")
	}
}

func TestRunRequiresDSN(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run(t.Context(), nil, strings.NewReader(""), &out, &diagnostics); code != 1 || diagnostics.String() != "schemigo: -d is required\n" {
		t.Fatalf("code=%d stderr=%q", code, diagnostics.String())
	}
}

func TestRunRejectsUnsupportedDatabase(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run(t.Context(), []string{"-d", "sqlite://user:secret@localhost/db"}, strings.NewReader(""), &out, &diagnostics); code != 1 || !strings.Contains(diagnostics.String(), "unsupported") || strings.Contains(diagnostics.String(), "secret") {
		t.Fatalf("code=%d stderr=%q", code, diagnostics.String())
	}
}

func TestRunRecognizesMySQLScheme(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := run(t.Context(), []string{"-d", "mysql://user:secret@localhost/db", "-history"}, strings.NewReader(""), &out, &diagnostics)
	if code != 1 || strings.Contains(diagnostics.String(), "unsupported database URL scheme") || strings.Contains(diagnostics.String(), "secret") {
		t.Fatalf("code=%d stderr=%q", code, diagnostics.String())
	}
}

func TestRunCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, diagnostics bytes.Buffer
	if code := run(ctx, []string{"-d", "postgres://user:secret@localhost/db"}, strings.NewReader(""), &out, &diagnostics); code != 1 || strings.Contains(diagnostics.String(), "secret") {
		t.Fatalf("code=%d stderr=%q", code, diagnostics.String())
	}
}

func TestRunErrorsReturnOne(t *testing.T) {
	for _, args := range [][]string{
		{"-unknown"},
		{"-d", "postgres://user:topsecret@127.0.0.1:bad/db", "-history"},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(t.Context(), args, strings.NewReader(""), &out, &diagnostics); code != 1 || diagnostics.Len() == 0 || strings.Contains(diagnostics.String(), "topsecret") {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, diagnostics.String())
		}
	}
}

func TestConfirmMigrationStopsOnFailedCheck(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	failure := errors.New("relation does not exist")
	proceed, err := confirmMigration(strings.NewReader("c\ny\n"), &out, false, func() error { return failure })
	if proceed || !errors.Is(err, failure) || !strings.Contains(err.Error(), "check migration:") ||
		out.String() != "Apply migration? (y = apply, n = cancel, c = check): " {
		t.Fatalf("proceed=%v output=%q error=%v", proceed, out.String(), err)
	}
}

func TestClickHouseApplyRequiresKeeper(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := run(t.Context(), []string{"-d", "clickhouse://user:secret@127.0.0.1:9000/default", "-yes"}, strings.NewReader(""), &out, &diagnostics)
	if code != 1 || !strings.Contains(diagnostics.String(), "-keeper") || strings.Contains(diagnostics.String(), "secret") {
		t.Fatalf("code=%d stderr=%q", code, diagnostics.String())
	}
}
