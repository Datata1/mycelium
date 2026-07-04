# WS03 — Why-Empty Hints (query-time diagnosis + result envelopes)

Size: **L**. Depends on: 01 (`last_full_scan_at` for staleness hints).
Soft-ordered after 04 (same renderer files; doing 04 first avoids churn).
Ships independently otherwise.

## Problem

When a query comes back empty, the agent cannot distinguish "no such thing"
from "file excluded by config" from "index is stale" — so it silently falls
back to grep and, having been burned once, stops trusting the tool. Today:

- Only `find_symbol` has a `Hints` envelope (`internal/ipc/results.go:41-44`,
  built by `buildFindHints` in `internal/query/hints.go`) and `read_focused`
  a preview `Hint`. Everything else returns bare empties: "no references"
  (`render/refs.go:16`), "no matches" (`render/search.go:17`), "no files",
  "no symbols", "no callers found", "no path found".
- The `suggestPaths` "Did you mean" helper (`internal/query/suggest.go`) is
  wired only into ReadFocused/SearchLexical *error* paths.
- `search_lexical` greps index-known paths: a file on disk but not indexed
  is invisible; a file in the index but deleted from disk logs to daemon
  stderr only (`internal/query/lexical.go:67-78`).
- Decision (2026-07-04): **no .gitignore parsing** — the walker stays
  config-glob driven; instead, empty results must explain *why* a file is
  not indexed.

## A. Shared probe — `internal/query/freshness.go`

`FSProbe{Root string; Include, Exclude []string; MaxFileSizeKB int}` with
`DiagnosePath(rel string) []string`, producing in order:

1. `os.Stat` miss → "not on disk either (deleted or never existed)".
2. Exists but matches an exclude glob → name the pattern:
   "matches exclude pattern `**/testdata/**` — excluded from indexing by
   .mycelium.yml" (reuse the walker's doublestar matching).
3. Exists but no include glob matches → "extension not covered by include
   globs (configured languages: go, typescript, python)".
4. Exists and rules pass → the money hint: **"file exists on disk but is not
   indexed — index is stale (file mtime X, last full scan Y); is the daemon
   running? `myco index` reconciles."** (`last_full_scan_at` from
   `index_meta`, WS01.)
5. Oversize (> MaxFileSizeKB) → say so.

Wiring: optional `Reader.SetProbe(*FSProbe)`; populated in
`service.NewReadOnly` (extend its signature; config values from `rc.Cfg` at
each call site — `cmd_daemon.go`, `cmd_query.go`; doctor/init call sites may
pass nil). Nil probe = today's behavior, so existing `internal/query` unit
tests stay untouched. Hints stay inside `internal/query` — the sole reader.

## B. Envelope changes (wire change; precedent: `FindSymbolResult` in v3.1)

Per-tool `Hints []string` fields, not a generic wrapper — each migration
reviewable alone, JSON tags stay the compat contract.

- **`get_references`** → `ipc.GetReferencesResult{Matches []ReferenceHit,
  Hints []string}`. In `Reader.GetReferences`: 0 symbol ids **and** nothing
  textual → `no symbol or reference named "X" in the index —
  find_symbol("X") does substring matching; check spelling/qualification`.
  Symbol exists but 0 refs → `symbol exists (N definitions) but has no
  indexed references — possibly reflection/codegen or dead code;
  get_neighborhood("X") shows outbound edges`. This kills the most
  misleading empty in the product ("no references" ≠ "nobody calls this").
- **`search_lexical`** → `ipc.SearchLexicalResult{Matches []LexicalHit,
  Hints []string}`. On 0 hits: identifier-shaped pattern
  (`^[A-Za-z_][A-Za-z0-9_.]*$`) → `if "X" is a symbol name,
  find_symbol("X") searches the code graph and catches qualified
  forms/renames`. Collect ENOENT counts from the `scanFile` workers
  (`lexical.go:67-78`, today stderr-only — thread a counter) → `N index
  entries missing on disk — index is stale; run myco index`. When
  `path_contains` names an on-disk-but-unindexed file, append the probe
  diagnosis.

Blast radius per envelope: `Service` method signature (`internal/service`),
CLI runner (`cmd/myco/cmd_query.go`), renderer (render Hints like
`symbols.go:16-20` does for find_symbol), golden fixtures,
`internal/daemon/equivalence_test.go`, mcpschema description update,
CHANGELOG breaking-change note (FindSymbol made the same move in v3.1).

## C. Error paths and static empty texts

- `GetFileOutline` / `GetFileSummary`: unknown path → error with
  `suggestPaths` ("Did you mean:") + `DiagnosePath` lines, via a shared
  path-resolution helper so it is written once. Critically, distinguish
  "file not indexed" (error + diagnosis) from "file indexed, zero symbols"
  (legitimate empty).
- `read_focused` notFound (`internal/query/read.go:77`): append
  `DiagnosePath` lines after the existing path suggestions.
- `find_document_key` on empty: list what IS indexed via existing
  `DocumentsByKind` — `no document entries for "X" — indexed kinds:
  json(40), go.mod(1); keys match by substring`. (The tool is invisible
  today; an empty that teaches its domain recruits future calls.)
- Renderer-only upgrades (no wire change):
  - `render/search.go` "no files" → `no files match — drop
    name_contains/language filters, or run stats to see indexed languages`.
  - `render/refs.go` "no callers found" → append ` — if unexpected, stats
    shows the unresolved-refs ratio for this repo`.
  - `render/refs.go` "no path found" → append ` — paths follow *outbound*
    call edges from 'from'; try swapping from/to, or get_neighborhood on
    either end`.
- `find_symbol`: one additional hint when 0 matches and `last_full_scan_at`
  is old or missing ("index may be stale/never reconciled"). No disk
  probing — a symbol name gives no path to stat.

## Risks

- Envelope changes break raw-JSON consumers (HTTP passthrough) — same as
  the v3.1 FindSymbol precedent; CHANGELOG note + version bump.
- Probe stats on every empty result add I/O — bounded: at most a handful of
  `os.Stat` calls, only on the miss path.
- Hint text drift vs reality — hint wording lives in pure functions next to
  `buildFindHints` (`hints.go`) so tests pin the exact strings.

## Tests

- Pure hint-builder unit tests (`hints_test.go` style).
- Integration (`test/integration/`): delete a file post-index →
  search_lexical returns the missing-on-disk hint; create an un-included
  `.md` file → probe "extension not covered" hint; `read_focused` on an
  excluded `testdata/` path → exclude-pattern hint.
- Golden renders: regenerate `search_lexical`/`get_references` fixtures,
  add empty-with-hints cases; `TestToolParity` and the equivalence suite
  verify the new envelopes across transports.
