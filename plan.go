package schemigo

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Statement is a complete SQL command, possibly containing internal semicolons.
// Destructive marks possible data loss; SkipExplain marks commands such as DDL
// which cannot be validated using EXPLAIN.
type Statement struct {
	SQL         string `json:"sql"`
	Destructive bool   `json:"destructive"`
	SkipExplain bool   `json:"skip_explain"`
	// OutsideTransaction requires autocommit before subsequent statements, for
	// example so newly added PostgreSQL enum values become visible.
	OutsideTransaction bool `json:"outside_transaction"`
}

// Plan contains SQL statements in execution order. Its zero value is an empty plan.
type Plan struct {
	Statements []Statement `json:"statements"`
}

// SQL returns commands in order, each terminated by a semicolon and newline.
// Surrounding whitespace and empty commands are omitted; command bodies are preserved.
// Execute Statements respecting OutsideTransaction; this rendering does not
// encode transaction boundaries.
func (p Plan) SQL() string {
	var out strings.Builder
	for _, statement := range p.Statements {
		command := strings.TrimSpace(statement.SQL)
		if command == "" {
			continue
		}
		out.WriteString(command)
		if !strings.HasSuffix(command, ";") {
			out.WriteByte(';')
		}
		out.WriteByte('\n')
	}
	return out.String()
}

// SHA256 returns the lowercase hexadecimal SHA-256 of the exact bytes of SQL().
func (p Plan) SHA256() string {
	hash := sha256.Sum256([]byte(p.SQL()))
	return hex.EncodeToString(hash[:])
}
