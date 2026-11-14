// Package mysql implements MySQL schema inspection and migrations.
package mysql

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

// Driver inspects MySQL schemas and plans their migrations.
type Driver struct{}

var _ schemigo.Driver = (*Driver)(nil)

// New returns a MySQL driver. The caller owns its database connections.
func New() schemigo.Driver { return &Driver{} }

// Name returns the dialect identifier.
func (*Driver) Name() string { return "mysql" }

// QuoteIdent quotes a MySQL identifier, including reserved words.
func QuoteIdent(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

// Qualified quotes a database and object name.
func Qualified(schema, name string) string { return QuoteIdent(schema) + "." + QuoteIdent(name) }

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "'", "''") + "'"
}

// Open parses a MySQL URL and returns a caller-owned connection pool.
func Open(dsn string) (*sql.DB, error) {
	return nil, fmt.Errorf("open mysql: driver dependency is not configured")
}
