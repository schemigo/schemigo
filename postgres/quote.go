package postgres

import "strings"

// QuoteIdent quotes one PostgreSQL identifier, including embedded double quotes.
// Callers must reject NUL and names longer than PostgreSQL's identifier limit.
func QuoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// Qualified quotes the namespace and object as separate identifiers.
func Qualified(schema, name string) string { return QuoteIdent(schema) + "." + QuoteIdent(name) }

// QuoteLiteral quotes a SQL string independently of standard_conforming_strings.
// NUL is not representable in PostgreSQL text and must be rejected by callers.
func QuoteLiteral(value string) string {
	value = strings.ReplaceAll(value, "'", "''")
	if strings.Contains(value, `\`) {
		return `E'` + strings.ReplaceAll(value, `\`, `\\`) + "'"
	}
	return "'" + value + "'"
}
