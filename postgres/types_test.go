package postgres_test

import (
	"testing"

	"github.com/schemigo/schemigo/postgres"
)

func TestNextval(t *testing.T) {
	tests := []struct{ name, table, column, want string }{
		{name: "simple", table: "users", column: "id", want: `nextval('"users_id_seq"'::regclass)`},
		{name: "quoted", table: `User"Archive`, column: "it's.id", want: `nextval('"User""Archive_it''s.id_seq"'::regclass)`},
		{name: "backslash", table: `a\b`, column: "id", want: `nextval(E'"a\\b_id_seq"'::regclass)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgres.Nextval(tt.table, tt.column); got != tt.want {
				t.Fatalf("Nextval = %q, want %q", got, tt.want)
			}
		})
	}
}
