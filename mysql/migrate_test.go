package mysql_test

import (
	"context"
	"testing"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/mysql"
)

func TestMySQLValidateRejectsEmptyStatement(t *testing.T) {
	if err := mysql.New().Validate(t.Context(), nil, schemigo.Statement{}); err == nil {
		t.Fatal("empty statement accepted")
	}
}

func TestMySQLValidateHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := mysql.New().Validate(ctx, nil, schemigo.Statement{SQL: "CREATE TABLE t (id INT)", SkipExplain: true}); err == nil {
		t.Fatal("canceled validation succeeded")
	}
}
