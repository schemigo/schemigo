package schemigo

import (
	"context"
	"database/sql"
	"time"
)

// Migration is a history event. Hash is the Plan.SQL digest, AppliedAt is the
// application time, and User identifies the actor. Hash need not be unique.
type Migration struct {
	Hash      string    `json:"hash"`
	AppliedAt time.Time `json:"applied_at"`
	User      string    `json:"user"`
}

// UnlockFunc releases a migration lock using the supplied context.
type UnlockFunc func(context.Context) error

// Driver implements a dialect on a caller-owned connection. Inspect reads one
// namespace; Diff plans changes from current to desired without applying them.
// Validate checks a command; Lock serializes migrations for a namespace.
// EnsureHistory creates the history store; HasMigration queries past events,
// RecordMigration appends an event, and ListMigrations reads history.
// Callers must keep the connection open through lock release. A driver backed by
// an external lease can additionally implement MigrationContext(context.Context)
// (context.Context, context.CancelFunc); Apply uses it after Lock to stop work
// when ownership is lost. It cannot fence commands already accepted by the server.
type Driver interface {
	Name() string
	Inspect(context.Context, *sql.Conn, string) (Schema, error)
	Diff(context.Context, *sql.Conn, Schema, Schema) (Plan, error)
	Validate(context.Context, *sql.Conn, Statement) error
	Lock(context.Context, *sql.Conn, string) (UnlockFunc, error)
	EnsureHistory(context.Context, *sql.Conn, string) error
	HasMigration(context.Context, *sql.Conn, string, string) (bool, error)
	RecordMigration(context.Context, *sql.Conn, string, Migration) error
	ListMigrations(context.Context, *sql.Conn, string) ([]Migration, error)
}
