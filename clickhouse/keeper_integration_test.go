//go:build integration

package clickhouse_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/schemigo/schemigo/clickhouse"
	"github.com/schemigo/schemigo/internal/testdb"
)

func TestClickHouseConcurrentLock(t *testing.T) {
	endpoint := testdb.Keeper(t)
	owner, err := clickhouse.NewKeeperLocker(t.Context(), []string{endpoint}, "/schemigo")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	other, err := clickhouse.NewKeeperLocker(t.Context(), []string{endpoint}, "/schemigo")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	unlock, err := owner.Lock(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if release, err := other.Lock(waiting, "default"); err == nil {
		_ = release(t.Context())
		t.Fatal("independent client entered held lock")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait: %v", err)
	}
	if err := unlock(t.Context()); err != nil {
		t.Fatal(err)
	}
	release, err := other.Lock(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := release(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Closing the owner's session must remove its ephemeral contender.
	if _, err := owner.Lock(t.Context(), "default"); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel2 := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel2()
	release, err = other.Lock(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
