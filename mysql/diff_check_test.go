package mysql

import (
	"testing"

	"github.com/schemigo/schemigo"
)

func TestSameCheck(t *testing.T) {
	catalog := schemigo.Check{Name: "chk_status", Expression: "(`status` <> _utf8mb4\\'\\')"}
	desired := schemigo.Check{Name: "chk_status", Expression: "status <> ''"}
	if !sameCheck(catalog, desired) {
		t.Fatal("MySQL CHECK formatting caused drift")
	}
	for _, changed := range []string{"status <> '_utf8mb4'", "status <> '`'", "status <> 'A'"} {
		desired.Expression = changed
		if sameCheck(catalog, desired) {
			t.Fatalf("changed CHECK %q was treated as unchanged", changed)
		}
	}
}
