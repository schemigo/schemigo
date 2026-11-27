# Go Development Guide

## Go Version and Standard Library

- Treat the `go` directive in `go.mod` as the single source of truth for the target Go version.
- Use language features and standard library APIs available in that version (`any`, `min`/`max`, `clear`, `slices`, `maps`, `cmp`, iterators, etc.) instead of hand-rolled equivalents or `interface{}`.
- Stay strictly within APIs supported by the `go.mod` version (enforced by `stdversion`).

## Style and Design

- Write simple, flat, idiomatic Go: early returns (guard clauses), minimal nesting, and short, clear names scoped to the package name.
- Prefer direct implementations over speculative abstraction layers, wrappers, or premature generalization.
- Make zero values useful and safe wherever natural.
- Handle or return every error explicitly; wrap with `fmt.Errorf("...: %w", err)` at boundaries that add meaningful context.
- Manage resources and concurrency explicitly: pass `context.Context` as the first parameter, always `defer cancel()`, close channels from the sender side, and ensure every goroutine has a clear exit path.

## Performance and Allocations

- Minimize allocations on hot paths and inside loops:
  - Preallocate slices and maps (`make([]T, 0, n)`, `make(map[K]V, n)`) when sizes are known or estimable.
  - Build strings and byte sequences with `strings.Builder` / `bytes.Buffer` and `Grow` instead of loop concatenation or avoidable `fmt.Sprintf`.
  - Avoid redundant `string` ↔ `[]byte` conversions, unnecessary boxing into `any`, and copying large structs by value.
  - Reuse buffers (or use `sync.Pool`) where it measurably reduces GC pressure.
- Prefer compact data layouts: use slices of values over slices of pointers unless shared reference semantics are needed.
- Verify non-obvious optimizations with `go test -bench=. -benchmem` before trading readability for speed.

## Interfaces

- Introduce an interface only when warranted: multiple real implementations or a concrete I/O, clock, or external-dependency seam for test isolation.
- Define interfaces on the consumer side and keep them small (1–3 methods).
- Accept and return concrete types by default; widen a parameter to an interface only when a concrete need arises.

## Local CLI Build

From the repository root, build `schemigo` into the `bin` directory under `GOPATH`:

```sh
go build -o "$(go env GOPATH)/bin/schemigo" ./cmd/schemigo
```

## Verification Before Completion

A task is complete only when all checks pass with zero errors or warnings in touched code:

1. `go vet ./...`
2. `staticcheck ./...`
3. `gopls` diagnostics with `"staticcheck": true` and the exact analyzer profile via LSP `textDocument/diagnostic` against `gopls serve` (plain `gopls check` and the `go_diagnostics` MCP tool run with default settings and skip `"staticcheck": true` analyzers such as `QF1001` as well as custom `analyses` overrides):
   - `"staticcheck": true`
   - Enabled (`true`): `unusedparams`, `nilness`, `unusedwrite`, `unusedvariable`, `unreachable`, `useany`, `lostcancel`, `printf`, `unusedresult`, `unusedfunc`, `unusedtypeparams`, `structtag`, `copylocks`, `stdversion`.
   - Disabled (`false`): `shadow` — local variable shadowing (such as `err` in inner blocks) is allowed.
   - Always uphold `useany` (`any` over `interface{}`) and `unusedtypeparams` (no unused type parameters), even when a given `gopls` version folds them into other analyzers.
4. `go test -race ./...` (scope to changed packages while iterating; run the full module before finishing).
