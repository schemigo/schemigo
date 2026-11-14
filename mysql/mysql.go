// Package mysql implements MySQL schema inspection and migrations.
package mysql

import (
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"
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
	cfg, err := parseURL(dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	connector, err := gomysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	return sql.OpenDB(connector), nil
}

func parseURL(dsn string) (*gomysql.Config, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "mysql" || u.User == nil || u.User.Username() == "" || u.Hostname() == "" || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/") || strings.Contains(strings.TrimPrefix(u.Path, "/"), "/") {
		return nil, fmt.Errorf("expected mysql://user:password@host/database")
	}
	database := strings.TrimPrefix(u.Path, "/")
	if err := mysqlName(database); err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		port = "3306"
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("invalid MySQL port")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid MySQL URL query: %w", err)
	}
	cfg := gomysql.NewConfig()
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(u.Hostname(), port)
	cfg.DBName = database
	if len(query) > 0 {
		cfg, err = gomysql.ParseDSN(cfg.FormatDSN() + "?" + query.Encode())
		if err != nil {
			return nil, fmt.Errorf("invalid MySQL options: %w", err)
		}
	}
	cfg.ParseTime = true
	cfg.MultiStatements = true
	return cfg, nil
}
