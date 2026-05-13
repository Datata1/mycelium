# C2 — Rust language support (parser + resolver)

**Priority:** P0 for v4 Phase 3 — second main user-visible v4 feature
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Depends on:** v4-f2 (Axum field test — confirms language pick AND scope)

## Goal

Add Rust to the supported-languages list. Mycelium currently parses
Go (stdlib `go/ast`), TS (tree-sitter), Python (tree-sitter). Rust
extends the tree-sitter list with a fourth language and a fourth
resolver.

After this ticket: a Rust file in `myco`-indexed repos shows up in
`stats by language: rust: <count>`, `find_symbol` resolves Rust
function/struct/trait names with proper module-path qualification,
`get_references` follows `use` statements and method calls.

**This ticket is parameterised.** F2's findings doc (`tickets/v4-f2-findings.md`)
may recommend a different language (Java/Spring, C++, etc.) — if so,
this ticket's text adapts but the structure stays. Confirm the
language pick at the top of the ticket before starting.

## Scope cut

A v1.0-quality Rust parser would cover everything tree-sitter-rust
parses. That's overkill for v4. The cut, mirroring v1.3's TS / Python
80%-of-tsc cuts:

**In:**
- `fn`, `pub fn`, `async fn` — function declarations
- `struct`, `enum`, `trait`, `impl` blocks — types
- `mod` declarations + module file resolution
- `use` statements — including `use foo::{bar, baz}` and
  `use foo::bar as qux` aliasing
- Method calls (`x.foo()`) and free function calls (`foo()`)
- Path-qualified calls (`foo::bar::baz()`)

**Out (deferred to v4.1+):**
- Macro expansion (`macro_rules!`, proc macros). Mycelium indexes
  the macro *invocation* as a textual ref; the macro body's
  generated symbols are invisible. Document.
- Generics-aware resolution (`Vec<T>`, trait bounds). Bounded:
  resolve to the trait, not the concrete impl.
- Lifetime annotations — irrelevant to graph queries.
- Conditional compilation (`#[cfg(...)]`) — index everything;
  doctor warning if a build profile excludes a lot.

This is the same shape as the TS/Python parsers' "v1.3 cut" — see
`internal/parser/typescript/walker.go` for prior art.

## Critical files

- `internal/parser/rust/` — new package.
  - `parser.go` — top-level entry, calls tree-sitter and walks the
    parse tree to emit `parser.Symbol` and `parser.Ref` slices.
  - `walker.go` — the actual AST traversal; mirrors
    `internal/parser/python/walker.go`.
  - `testdata/` — fixtures: `simple.rs`, `with_uses.rs`,
    `axum_router.rs`, `traits_and_impls.rs`. Each gets a snapshot
    test asserting the emitted symbol shape.
- `internal/parser/parser.go` — register `"rust"` and `.rs` /
  `.rs.in` extensions.
- `internal/resolver/rust/` — new package.
  - `resolver.go` — `ResolveRefs(ctx, ix) error`. Walks the
    `refs` table for Rust files; resolves textual matches against
    the symbol table using use-tree + module-path resolution.
  - Uses `ResolverVersion = 4` (Go=1, TS=2, Py=3 today).
- `internal/index/migrations/` — **no schema change required.**
  ResolverVersion is a value, not a column rename.
- `internal/parser/tsutil/` — if any tree-sitter helpers are
  shared with TS/Python, factor up. Otherwise duplicate the
  pattern; tree-sitter glue is small.
- `internal/doctor/checks.go` — add `rust_parse_errors` check
  mirroring the existing TS / Python loaders.
- `cmd/myco/main.go` — register the Rust parser in the daemon
  startup wiring.
- `Taskfile.yml` — add `rust-fixtures` task to (re)generate
  testdata expected output if the snapshot pattern uses a
  generator.

## Tree-sitter dependency

Use `github.com/smacker/go-tree-sitter` (already in `go.mod` for
TS/Python) with the Rust grammar. The Rust grammar is large but
well-maintained. Add to imports; no new top-level dep.

## Acceptance criteria

- `task check` passes; race-free.
- `myco index` on a small Rust repo (e.g. `tokio-rs/axum`'s
  `examples/key-value-store`) completes without errors,
  `myco doctor` reports zero LoadErrors, zero unresolved refs
  (or in band with TS / Python rates: < 5%).
- `find_symbol` resolves:
  - Free functions: `find_symbol{name: "handler"}` finds an
    `async fn handler(...)`.
  - Methods on structs: `find_symbol{name: "new"}` returns one
    match per `impl` block.
  - Trait methods: `find_symbol{name: "fmt"}` finds `Display::fmt`
    impls.
- `get_references{target: "handler"}` follows method-call edges
  including `use` aliases.
- `myco stats` shows `by language: rust: <count>`.
- New entry in LIMITATIONS.md documenting the
  generics + macros caveats.
- README's supported-languages list updated.
- Self-index of mycelium itself stays clean (no Rust files in
  mycelium's tree, but the build/test cycle should not regress
  Go / TS / Python — `task smoke` green).

## What this enables

- **Rust users get the agent-native story.** Same MCP surface as
  Go / TS / Python users.
- **C1's Rust route_constructors** (Axum) actually resolves
  routes to handlers — without C2, Axum routes would emit
  symbols but with no handler refs because the parser doesn't
  exist.
- **F2's findings stop being theoretical.** Re-running F2 with
  Rust parser support shows the adoption delta.
- **Foundation for v4.1+ language additions** — the parser /
  resolver scaffolding pattern is now four-replicated, ready to
  fork for the next language.

## Out of scope

- **Cargo workspace awareness.** v4 treats a workspace as a flat
  set of files. Per-crate scoping (analogous to v1.5's
  per-project scoping) is v4.1+.
- **Build-script-generated code.** `build.rs` outputs to
  `OUT_DIR`; mycelium indexes them as plain `.rs` files but
  doesn't follow the build script's logic. Same as Go's
  generated code today.
- **Inline tests** (`#[cfg(test)] mod tests { ... }`). Indexed as
  symbols; no special test-aware tooling. Optional v4.1+ work.
- **`async fn` lifetime / `Pin<Box<...>>` future-type
  resolution.** Out of scope; resolved as the function symbol
  without future-type metadata.

## Honest caveats

- Tree-sitter-rust occasionally lags rustc on cutting-edge syntax
  (let-else chains, RFC-fresh features). Real repos will have
  parse errors on cutting-edge code. The doctor `rust_parse_errors`
  check surfaces this; users can pin a tree-sitter-rust version
  in their `.mycelium.yml` if they need to.
- Macro invocations are an Achilles heel. `tracing::info!(...)`
  and similar are textual refs — the macro's expanded symbols
  don't exist in the graph. Real Rust codebases lean heavily on
  macros; expect the unresolved-ref ratio to be higher than Go's.
  Document and live with it for v4.
- The 80%-of-rustc cut is a guess at what's worth shipping. F2's
  findings should validate; if F2 surfaces a "scope walker
  doesn't handle X and X is everywhere" finding, narrow the
  ticket scope and ship X in v4.1.
