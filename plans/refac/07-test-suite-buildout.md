# 07 — Test Suite Buildout

**Size:** L (spread across the whole roadmap) · **Depends on:** phased —
A: nothing, B: 03, C: 04/05 · **Blocks:** nothing

## Goal

Unit-level confidence underneath the strong integration suite, with golden
files for every rendered-text surface. Seams first (delivered by WS03–05),
tests immediately behind each seam.

## Pain

Zero unit tests for: `internal/query` (2958 LOC — the core), `daemon`, `http`,
`ipc`, `index`, `config`, all `parser/*` and `resolver/*`, `repo`, `hook`,
`mcp/render`, and all of `cmd/myco`. No golden files despite the product
surface being rendered text. Every refactor is currently guarded only by the
8 integration suites.

## Design

### Phase A — no seams needed (start immediately, parallel with everything)

- **`internal/ipc`**: request/response marshalling round-trips; error-code ↔
  sentinel mapping (extends WS02's tests).
- **`internal/parser/*`**: table-driven — source snippet in, expected
  `Symbol`/`Reference` structs out. Parsers are plain structs by design, so
  these are the cheapest high-value tests in the repo, and they double as
  documentation for language contributors (WS06's doc links them).
- **`internal/config`**: two-tier merge cases (defaults → user → repo),
  validation errors.
- **`internal/mcp/render`: golden files.** Layout
  `internal/mcp/render/testdata/golden/<method>/<case>.txt`; canonical ipc DTO
  fixtures in, rendered string out. Convention:

  ```go
  var update = flag.Bool("update", false, "rewrite golden files")
  ```

  `go test ./internal/mcp/render -update` regenerates; CONTRIBUTING says
  "review golden diffs like code".
- **CLI output**: before WS04, drive `myco` as a subprocess in
  `test/integration` against the existing testdata index; after WS04, golden
  tests at the render step of the shared dispatch.

### Phase B — after WS03 (store seam)

- **`internal/query` unit tests** against **file-backed SQLite in
  `t.TempDir()`** — not `:memory:` (cgo driver + shared-cache memory DBs have
  cross-connection foot-guns, and production uses file DBs). Tests require
  `-tags sqlite_fts5` (CI default); add a clear skip/fail message for
  tag-less runs so `go test ./...` without tags fails loudly, not weirdly.
- **Fixture builder** `internal/query/querytest`:
  - direct row inserts through `internal/index` for precise, fast **store**
    tests;
  - the real pipeline over a tiny fixture tree (reuse
    `test/integration/testdata`) for **Reader**-level tests.
- Pure-function tests for the ranking/diagnosis/preview logic extracted in
  WS03.
- **Formalize query hot-path benchmarks** (CLAUDE.md mandate) next to these
  fixtures; record baselines in the repo (a `BENCHMARKS.md` or the plan doc).

### Phase C — after WS04/05 (service + registry seams)

- **Daemon dispatch tests**: Service over a fixture DB, unix socket in
  `t.TempDir()` — request bytes in, response bytes out, including error codes
  and unknown methods.
- **`internal/http`**: `httptest` against the same dispatcher.
- **Dual-path equivalence suite** (owned by WS04, maintained here): every
  query subcommand with daemon up vs. down, identical stdout.
- **Registry parity test** (owned by WS05, maintained here).

### Non-goals

- No mocking framework — hand-written fakes over the existing small
  consumer-side interfaces.
- No `resolver/*` internals tests beyond parser-level + integration coverage
  (marginal value is low; revisit if a resolver bug escapes).
- No coverage ratchet/threshold gate — add `-coverprofile` to the CI test job
  and treat coverage as informational; record the starting baseline here when
  Phase A lands.

## Migration path

Phase A in small parallel PRs immediately; B and C gated as above. Each WS03
store-extraction PR and each WS04 handler-conversion PR ships its tests in the
same PR — tests are not a trailing cleanup.

## Risks

- Golden files rot if `-update` is run carelessly — the CONTRIBUTING note is
  the mitigation; reviewers treat golden diffs as code.
- cgo test compilation on macOS runners is slow — keep fixture DBs tiny and
  share the built test binary per package (default `go test` behavior; avoid
  per-test process spawning except the subprocess CLI suite).

## Exit criteria

- Every package named in the pain statement has at least one meaningful test
  file.
- `internal/mcp/render` fully golden-covered (all 12 methods).
- Every store method in `internal/query` covered; hot-path benchmarks exist
  with recorded baselines.
- Daemon dispatch and HTTP transport covered including error codes.
- CI publishes a coverage profile.
