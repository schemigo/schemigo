package postgres

import (
	"regexp"
	"testing"
)

func TestExpressionComparison(t *testing.T) {
	for _, tt := range []struct {
		name, a, b string
		equal      bool
	}{
		{name: "formatting", a: "(id >= 0)", b: "id>=0", equal: true},
		{name: "grouping", a: "(a)+(b)", b: "a+b"},
		{name: "strings", a: "name='a b'", b: "name='ab'"},
		{name: "identifiers", a: `"a b"`, b: `"ab"`},
		{name: "dollar strings", a: "$TAG$a b$TAG$", b: "$TAG$ab$TAG$"},
		{name: "comments", a: "a +/* nested /* x */ comment */ b", b: "a + b", equal: true},
		{name: "operator spacing", a: "a - - b", b: "a -- b"},
		{name: "word boundaries", a: "foo and bar", b: "fooandbar"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if equal := expressionKey(tt.a) == expressionKey(tt.b); equal != tt.equal {
				t.Fatalf("equal=%v, want %v", equal, tt.equal)
			}
			if !tt.equal && regexp.MustCompile(expressionPattern(tt.a)).MatchString(tt.b) {
				t.Fatal("runtime guard accepted a different expression")
			}
		})
	}
}
