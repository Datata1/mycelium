# 03 — Wire Types & Query Decomposition

**Size:** M/L · **Depends on:** 02 (sentinels land in the code being moved) ·
**Blocks:** 04, 05, 07-B

## Goal

Give the JSON wire contract an owned home (`internal/ipc`) and turn
`internal/query` from a 2958-LOC god type with embedded SQL into thin
orchestration over a testable store — **without changing a single byte of JSON
output**.

## Pain

- Result structs in `internal/query` double as the wire contract, so
  `internal/mcp/render`, the daemon, and the CLI all import query internals;
  any internal refactor risks the wire format silently.
- `Reader` mixes SQL text, row scanning, ranking/diagnosis logic, and file I/O
  (`ReadFocused` reads the repo from disk) across 13 files.
- Zero unit tests — there is no seam; everything needs a real `*sql.DB`.
- `query.go` alone is 811 lines.

## Constraints

`internal/query` **remains the sole reader** — the decomposition is internal
to the package. Transports still never see SQL.

## Design

### DTO home: `internal/ipc/results.go`

- Move the result structs (`FindSymbolResult`, reference hits, `Neighborhood`,
  impact/critical-path results, file summary, focused read, lexical hits,
  stats, …) **verbatim** into `internal/ipc` — JSON tags untouched. ipc is a
  dependency-free leaf that already owns the request half (`*Params`); params
  and results are one protocol and belong together.
- `internal/query` keeps **type aliases**
  (`type FindSymbolResult = ipc.FindSymbolResult`) so every existing import
  site compiles unchanged. Aliases may stay permanently as convenience.
- `internal/mcp/render` then imports only `internal/ipc` — decoupled from
  query internals.
- Deliberately **not** `pkg/`: the JSON shape is the public contract, not the
  Go structs. Promoting them would freeze Go-level API we don't want to
  promise.
- Rejected: a new `internal/apitypes` package — a second leaf next to ipc with
  an arbitrary params-here/results-there boundary.

### Reader → façade over an unexported store

- **Reader stays one type** (single obvious anchor for the sole-reader
  invariant). Internally:
  - `internal/query/store.go`: `type store struct{ db *sql.DB }` with narrow
    row-level methods (`symbolsByName`, `refsForSymbolIDs`, `edgesFrom`,
    `fileByPath`, …) returning plain row structs. **All SQL strings live
    here.** Start same-package unexported; promote to a subpackage only if it
    grows past usefulness.
  - Reader methods become: validate params → store calls → assemble/rank/
    diagnose → DTO.
  - Pure assembly logic (FindSymbol ranking, hint generation in `diagnose.go`,
    the preview logic around `read.go:355-372`) becomes free functions over
    plain data — unit-testable with zero DB.
  - `ReadFocused` gets an `fs.FS` seam (field on Reader, default rooted at the
    repo) so golden tests don't need a real checkout.
- Rejected: splitting into `SymbolReader`/`GraphReader`/`DocReader` — Go
  guidance is to split by *dependency*, not topic; every method shares the one
  dependency (the DB), and multiple types multiply wiring in daemon + CLI +
  tests for no consumer benefit.

### Guard against JSON drift

Before step 1, capture every integration-test response body as golden files;
after each step, assert **byte equality**. These goldens seed WS07.
Preserve nil-slice vs. empty-slice exactly (nil marshals `null`, empty `[]`)
— moving code can't change it, "improving" it accidentally can.

## Migration path (green at every step)

1. Capture golden responses (throwaway harness or the WS07 harness if ready).
2. Move DTOs to ipc + aliases in query — one purely mechanical PR; integration
   suite + goldens prove byte-identical output.
3. Point `mcp/render` and the daemon at the ipc types directly.
4. Extract store methods one Reader file at a time — order:
   `query.go` → `graph.go` → `neighborhood.go` → `lexical.go` → `read.go` →
   the rest. Each PR ships unit tests for the extracted pure logic (feeds
   WS07-B).
5. Retire aliases at leisure, or keep them.

## Risks

- **JSON drift** is the whole risk; the golden capture is the mitigation.
- Struct moves can silently drop an `omitempty` — golden byte-diffs catch it.
- Query hot-path performance: the store split must not add per-row
  allocations; re-run benchmarks (CLAUDE.md mandate).

## Verification

- Integration suite + golden byte-diff green after every PR.
- New store/pure-function unit tests pass without a daemon or checkout.
- Query benchmarks show no regression vs. the pre-refactor baseline (record
  the baseline numbers in the first PR).
