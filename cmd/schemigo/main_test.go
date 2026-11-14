package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestPrintSQLReportsWriteError(t *testing.T) {
	if err := printSQL(failingWriter{}, "SELECT 1\n"); err == nil {
		t.Fatal("SQL output error was ignored")
	}
}

func TestRunRequiresDSN(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run(t.Context(), nil, strings.NewReader(""), &out, &diagnostics); code != 1 || !strings.Contains(diagnostics.String(), "-d") {
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

func TestClickHouseApplyRequiresKeeper(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := run(t.Context(), []string{"-d", "clickhouse://user:secret@127.0.0.1:9000/default", "-yes"}, strings.NewReader(""), &out, &diagnostics)
	if code != 1 || !strings.Contains(diagnostics.String(), "-keeper") || strings.Contains(diagnostics.String(), "secret") {
		t.Fatalf("code=%d stderr=%q", code, diagnostics.String())
	}
}
