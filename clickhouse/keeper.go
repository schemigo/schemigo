package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-zookeeper/zk"
	"github.com/schemigo/schemigo"
)

type keeperLocker struct {
	conn   *zk.Conn
	path   string
	lost   context.Context
	cancel context.CancelFunc
	done   chan struct{}
	gate   chan struct{}
	once   sync.Once
}

// NewKeeperLocker connects to Keeper/ZooKeeper and creates a lock directory.
// All clients managing the same server must use the same endpoints and path.
// The path must be dedicated to schemigo on a trusted coordinator (world ACL).
// Any disconnect invalidates this locker permanently; construct a new one to retry.
// Cancellation stops later DDL, but cannot fence a query already accepted by ClickHouse.
func NewKeeperLocker(ctx context.Context, endpoints []string, root string) (Locker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(endpoints) == 0 || root == "/" || !strings.HasPrefix(root, "/") || path.Clean(root) != root || strings.ContainsRune(root, 0) {
		return nil, fmt.Errorf("Keeper requires endpoints and a clean absolute lock path")
	}
	for _, endpoint := range endpoints {
		if strings.TrimSpace(endpoint) == "" {
			return nil, fmt.Errorf("empty Keeper endpoint")
		}
	}
	conn, events, err := zk.Connect(endpoints, 10*time.Second, zk.WithLogInfo(false))
	if err != nil {
		return nil, fmt.Errorf("connect Keeper: %w", err)
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	connected := false
	for !connected {
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, ctx.Err()
		case <-timer.C:
			conn.Close()
			return nil, fmt.Errorf("Keeper session timeout")
		case event, ok := <-events:
			if !ok {
				conn.Close()
				return nil, fmt.Errorf("Keeper event stream closed")
			}
			connected = event.State == zk.StateHasSession
		}
	}
	lost, cancel := context.WithCancel(context.Background())
	l := &keeperLocker{conn: conn, path: root, lost: lost, cancel: cancel, done: make(chan struct{}), gate: make(chan struct{}, 1)}
	go func() {
		defer close(l.done)
		for {
			select {
			case <-lost.Done():
				return
			case event, ok := <-events:
				if !ok || event.State == zk.StateDisconnected || event.State == zk.StateExpired || event.State == zk.StateAuthFailed {
					cancel()
					return
				}
			}
		}
	}()
	for i := 1; i <= len(root); i++ {
		if i < len(root) && root[i] != '/' {
			continue
		}
		_, err = conn.Create(root[:i], nil, 0, zk.WorldACL(zk.PermAll))
		if err != nil && !errors.Is(err, zk.ErrNodeExists) {
			_ = l.Close()
			return nil, fmt.Errorf("create Keeper path: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		_ = l.Close()
		return nil, err
	}
	if lost.Err() != nil {
		_ = l.Close()
		return nil, fmt.Errorf("Keeper session lost")
	}
	return l, nil
}
func (l *keeperLocker) MigrationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancel(l.lost)
	stop := context.AfterFunc(ctx, cancel)
	if ctx.Err() != nil {
		cancel()
	}
	return leaseContext{Context: child, caller: ctx}, func() { stop(); cancel() }
}
func (l *keeperLocker) Close() error {
	l.once.Do(func() { l.cancel(); l.conn.Close(); <-l.done })
	return nil
}
func (l *keeperLocker) Lock(ctx context.Context, _ string) (schemigo.UnlockFunc, error) {
	ctx, cancel := l.MigrationContext(ctx)
	defer cancel()
	select {
	case l.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("Keeper lock unavailable: %w", ctx.Err())
	}
	releaseGate := true
	defer func() {
		if releaseGate {
			<-l.gate
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	node, err := l.conn.CreateProtectedEphemeralSequential(l.path+"/lock-", nil, zk.WorldACL(zk.PermAll))
	if err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("create Keeper contender: %w", err)
	}
	cleanup := func() error {
		err := l.conn.Delete(node, -1)
		if errors.Is(err, zk.ErrNoNode) {
			return nil
		}
		if err != nil {
			_ = l.Close()
		}
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = cleanup()
			return nil, fmt.Errorf("Keeper lock canceled or session lost: %w", err)
		}
		children, _, err := l.conn.Children(l.path)
		if err != nil {
			_ = cleanup()
			return nil, err
		}
		sort.Slice(children, func(i, j int) bool { return sequence(children[i]) < sequence(children[j]) })
		position := -1
		for i, n := range children {
			if n == path.Base(node) {
				position = i
				break
			}
		}
		if position < 0 {
			_ = cleanup()
			return nil, fmt.Errorf("Keeper contender disappeared")
		}
		if position == 0 {
			if err := ctx.Err(); err != nil {
				_ = cleanup()
				return nil, err
			}
			releaseGate = false
			var once sync.Once
			var unlockErr error
			return func(context.Context) error { once.Do(func() { unlockErr = cleanup(); <-l.gate }); return unlockErr }, nil
		}
		exists, _, watch, err := l.conn.ExistsW(l.path + "/" + children[position-1])
		if err != nil {
			_ = cleanup()
			return nil, err
		}
		if !exists {
			continue
		}
		select {
		case <-ctx.Done():
			_ = cleanup()
			return nil, fmt.Errorf("Keeper lock canceled or session lost: %w", ctx.Err())
		case <-watch:
		}
	}
}
func sequence(node string) string { i := strings.LastIndex(node, "-"); return node[i+1:] }

// Lease loss propagates synchronously; caller values and deadlines remain visible.
type leaseContext struct {
	context.Context
	caller context.Context
}

func (ctx leaseContext) Deadline() (time.Time, bool) { return ctx.caller.Deadline() }
func (ctx leaseContext) Value(key any) any           { return ctx.caller.Value(key) }

func (ctx leaseContext) Err() error {
	if err := ctx.Context.Err(); err != nil {
		if callerErr := ctx.caller.Err(); callerErr != nil {
			return callerErr
		}
		return err
	}
	return nil
}
