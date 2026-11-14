// Package clickhouse implements ClickHouse schema inspection and coordinated migrations.
package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/schemigo/schemigo"
)

// Locker coordinates migrations across independent clients. Close releases its session.
type Locker interface {
	Lock(context.Context, string) (schemigo.UnlockFunc, error)
	Close() error
}

// Driver plans ClickHouse DDL. Without a Locker it supports read-only operations.
type Driver struct{ locker Locker }

var _ schemigo.Driver = (*Driver)(nil)

// New returns a driver using locker, which remains owned by the caller.
func New(locker Locker) schemigo.Driver { return &Driver{locker: locker} }

// Open parses a clickhouse:// DSN without connecting. The caller owns the pool.
func Open(dsn string) (*sql.DB, error) {
	options, err := ch.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	return ch.OpenDB(options), nil
}

// Name returns the dialect identifier.
func (*Driver) Name() string { return "clickhouse" }

// QuoteIdent quotes one ClickHouse identifier.
func QuoteIdent(name string) string {
	return "`" + strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(name) + "`"
}

// Qualified quotes database and object separately.
func Qualified(database, name string) string { return QuoteIdent(database) + "." + QuoteIdent(name) }

func validName(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid ClickHouse identifier %q", name)
	}
	return nil
}
