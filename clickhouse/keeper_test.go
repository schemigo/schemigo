package clickhouse

import (
	"context"
	"testing"
	"time"
)

func TestKeeperValidation(t *testing.T) {
	for _, root := range []string{"", "/", "relative", "/a/../b", "/a/"} {
		t.Run(root, func(t *testing.T) {
			if _, err := NewKeeperLocker(t.Context(), []string{"localhost:2181"}, root); err == nil {
				t.Fatal("invalid path accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewKeeperLocker(ctx, []string{"localhost:2181"}, "/schemigo"); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestKeeperSessionLostCancelsMigration(t *testing.T) {
	lost, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &keeperLocker{lost: lost}
	ctx, stop := l.MigrationContext(t.Context())
	defer stop()
	cancel()
	<-ctx.Done()
	ctx2, stop2 := l.MigrationContext(t.Context())
	defer stop2()
	if ctx2.Err() == nil {
		t.Fatal("lost session accepted another migration")
	}
}

func TestKeeperMigrationContextPreservesCaller(t *testing.T) {
	type key struct{}
	caller, cancelCaller := context.WithTimeout(context.WithValue(t.Context(), key{}, "value"), time.Minute)
	defer cancelCaller()
	lost, cancelLost := context.WithCancel(context.Background())
	defer cancelLost()
	l := &keeperLocker{lost: lost}
	ctx, cancel := l.MigrationContext(caller)
	defer cancel()
	wantDeadline, _ := caller.Deadline()
	gotDeadline, ok := ctx.Deadline()
	if !ok || !gotDeadline.Equal(wantDeadline) || ctx.Value(key{}) != "value" {
		t.Fatal("caller metadata lost")
	}
	cancelLost()
	if ctx.Err() == nil {
		t.Fatal("session loss was not synchronous")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Err without closed Done")
	}
}
func TestKeeperMigrationContextCallerCancellation(t *testing.T) {
	lost, cancelLost := context.WithCancel(context.Background())
	defer cancelLost()
	caller, cancelCaller := context.WithCancel(t.Context())
	l := &keeperLocker{lost: lost}
	ctx, cancel := l.MigrationContext(caller)
	defer cancel()
	cancelCaller()
	<-ctx.Done()
	if ctx.Err() == nil {
		t.Fatal("caller cancellation ignored")
	}
}

func TestKeeperMigrationContextPreservesDeadlineError(t *testing.T) {
	lost, cancelLost := context.WithCancel(context.Background())
	defer cancelLost()
	caller, cancelCaller := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelCaller()
	l := &keeperLocker{lost: lost}
	ctx, cancel := l.MigrationContext(caller)
	defer cancel()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("deadline changed into %v", ctx.Err())
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Err without closed Done")
	}
}
