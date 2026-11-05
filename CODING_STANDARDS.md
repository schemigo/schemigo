# Go Coding Standard

This document defines the coding rules for `schemigo`. It draws on the [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md), [Effective Go](https://go.dev/doc/effective_go), and [Go Doc Comments](https://go.dev/doc/comment), and adds project requirements for simplicity, extensibility, allocations, and tests. If the external guides conflict with this document, follow this document. Let `gofmt` handle formatting; judge other choices by readability, correctness, and measurement.

## Principles

1. **Simplicity.** Solve the current problem in the shortest clear way. Use the standard library and existing code first. Do not add a layer, interface, option, or dependency for a hypothetical future use case. If a few direct lines are easier to understand than a generic mechanism, keep the direct lines.
2. **Extensibility through clear boundaries.** Separate responsibilities when they have distinct reasons to change. Hide implementation details behind a small API and export only what callers need. Define an interface at the point of use when replacing an implementation is a real requirement. A new requirement should call for a local change without rewriting unrelated code.
3. **Idiomatic Go.** Prefer familiar language constructs and standard library APIs. Do not import patterns from other languages when Go offers a simpler approach. Preserve existing behavior and API contracts over stylistic uniformity.
4. **Few unnecessary allocations.** Avoid needless copies, conversions, and temporary objects, especially on hot paths. Optimize after profiling or benchmarking; do not sacrifice correctness or clarity for unproven savings. Copies needed for data ownership and race safety are required.
5. **Testable behavior.** Add tests for new behavior and bug fixes. Test observable results. Use unit tests for local logic and integration tests for interactions between components or external systems.

## Structure and APIs

- Keep packages focused. Use short, lowercase package names; avoid generic names such as `util` and `common`, and avoid repeating the package name in exported identifiers.
- Keep dependencies explicit and directed. Pass dependencies through constructors or parameters when this clarifies lifecycle management and testing. Avoid mutable globals and `init()` functions with side effects.
- Return concrete types; accept interfaces when callers genuinely need to supply different implementations. Do not use pointers to interfaces. Verify interface compliance at compile time when it is part of a public contract.
- Make a type's zero value useful where practical. If initialization is required, provide a constructor and document the constraint. Do not copy values containing a `sync.Mutex`.
- Do not embed a type in a public struct unless its methods should become part of that struct's API. Keep mutexes in unexported fields.

## Documentation

- Give each package one package comment in a source file. Start it with `Package name` and explain what the package does and when to use it. Keep the comment brief for a simple package; do not repeat it across files.
- Document every exported declaration with a doc comment immediately before it, without a blank line. Start the first sentence with the declared name (or `Package name` for a package). Explain exported struct fields either in the type comment or beside the fields.
- Write for callers: describe behavior, return values, errors callers may handle, edge cases, ownership or mutation of returned data, zero-value behavior, and concurrency guarantees when relevant. Avoid restating the signature or describing internal algorithms that can change without affecting the contract.
- Use ordinary `//` comments and complete sentences. Let `gofmt` format doc comments; use paragraphs, lists, links, and code blocks in the syntax supported by `go/doc/comment` rather than HTML or decorative Markdown.
- Add `ExampleXxx` tests for public APIs when a short usage example makes the contract clearer. Keep examples executable and verify their output when appropriate. Keep README or other user-facing guidance aligned with the code, without duplicating the full API reference.
- Update comments, examples, and user-facing documentation in the same change as the behavior or public API they describe. Check the rendered result with `go doc` when writing or changing a public API.

## Code and errors

- Format Go files with `gofmt`. Use clear names, a consistent style within each package, early returns, and narrow variable scope. Avoid shadowing built-in names without good reason.
- Handle errors where there is enough context to decide what to do. When returning an error, add useful context with `fmt.Errorf("operation: %w", err)`; handle each error once. Use `errors.Is` or `errors.As` for error checks, never string matching. Do not use `panic` for ordinary errors.
- Check type assertions with `value, ok := x.(T)` unless the type is guaranteed by the contract.
- Use `defer` near resource acquisition for cleanup. Handle close errors when they affect data integrity.
- Use `time.Time` for instants and `time.Duration` for intervals. Include units in names of external fields when the type does not express them.
- Add explicit tags to serialized struct fields so renaming a Go field cannot silently change the external format.
- Consider ownership when storing incoming slices or maps or returning internal ones. Copy at the boundary if callers could otherwise mutate internal state or cause a data race.
- Every goroutine needs a clear owner and a way to finish. Propagate `context.Context` for cancellation and provide a way to wait for background work to stop. Choose channel buffer sizes based on a known workload, not a guess.

## Allocations and performance

- Write clear code first. For a performance-sensitive path, record a baseline benchmark (`go test -bench=. -benchmem ./...`) and profile before optimizing, then compare the result.
- Set slice and map capacity when the expected size is known. Do not overallocate without evidence.
- Avoid repeated `string` ↔ `[]byte` conversions and `fmt` formatting when `strconv` or direct writing is sufficient. Apply these changes where the gain is measurable or the code is plainly simpler.
- Consider runtime, memory use, and allocation count together. Do not introduce object pools, manual buffer management, or complex caches without a demonstrated need.
- Benchmarks must check their results and use realistic inputs. When claiming an improvement, report `ns/op`, `B/op`, and `allocs/op`. Fewer allocations alone do not justify worse behavior or a worse API.

## Tests

- **Unit tests:** Cover the main cases, boundaries, and errors in changed logic. Test behavior and contracts rather than implementation steps. Use named subtests for a set of similar cases; do not build a table for a single case.
- **Integration tests:** Cover real boundaries that matter to behavior, such as serialization, the file system, a database, or an external protocol when those appear in the project. Replacing every component with a mock does not test integration. Isolate test data, release resources with `t.Cleanup`, state prerequisites clearly, and keep tests independent of execution order.
- If an integration test needs external infrastructure, separate it with `//go:build integration`, document the required environment variables, and run it separately. Tests that need no infrastructure must remain runnable with `go test ./...`.
- For concurrent code, test cancellation and goroutine shutdown, and run `go test -race ./...`. Do not use `time.Sleep` to synchronize tests.
- Inspect coverage of changed logic with `go test -cover ./...` and, when useful, `-coverprofile`. Use the percentage to find missing cases, not as a substitute for meaningful assertions. Do not write tests solely to raise a number.
- A bug fix must include a test that reproduces the previous failure. If the behavior depends on an external system, add the appropriate integration test.

## Checks before a PR

1. Review affected callers, public APIs, tests, and documentation. Check compatibility and data ownership.
2. Run `gofmt` on changed Go files, `go vet ./...`, and `go test ./...`.
3. For concurrent code, run `go test -race ./...`. For changes to real integration boundaries, run `go test -tags=integration ./...` in a prepared environment.
4. For performance changes, provide comparable before-and-after benchmarks. State the trade-offs if the new implementation is more complex.
