package postgres_test

import (
	"github.com/schemigo/schemigo/postgres"
	"testing"
)

func TestQuoteIdent(t *testing.T) {
	for _, tt := range []struct{ in, want string }{{"order", `"order"`}, {`User"Archive`, `"User""Archive"`}, {"a.b", `"a.b"`}} {
		t.Run(tt.in, func(t *testing.T) {
			if got := postgres.QuoteIdent(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	if got := postgres.Qualified(`a"b`, "user"); got != `"a""b"."user"` {
		t.Fatal(got)
	}
}

func TestQuoteLiteral(t *testing.T) {
	for _, tt := range []struct{ in, want string }{{"it's", "'it''s'"}, {`a\b`, `E'a\\b'`}, {"$$; DROP TABLE x; --", "'$$; DROP TABLE x; --'"}} {
		t.Run(tt.in, func(t *testing.T) {
			if got := postgres.QuoteLiteral(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
