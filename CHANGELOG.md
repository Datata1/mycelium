# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project adheres
to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [v1.4.0] — 2026-04-22

"Semantic at scale" — the fourth v2.0 milestone (Pillar B). Adds optional
[sqlite-vec](https://github.com/asg017/sqlite-vec) integration behind
runtime feature detection. Brute-force Go cosine stays as the honest
fallback; nothing breaks when the extension is missing.

### Added

- **`internal/index/vss.go`** — extension loader via a per-process named
  driver + `ConnectHook` that auto-loads the library on every new DB
  connection. `EnsureVSS(dim)` creates a `vss_chunks` virtual table
  named by dimension and backfills rows from any pre-existing
  `chunks.embedding`. `VSSAvailable()` and `VSSTableName()` let callers
  branch at query time.
- **`index.OpenWithExtension(path, extPath)`** — new opener that
  transparently handles both the extension-loaded and fallback cases.
  `index.Open(path)` keeps its pre-v1.4 behavior.
- **Dual-write in `WriteEmbedding`** — every embedding lands in both
  `chunks.embedding` (source of truth / fallback) and `vss_chunks`
  (KNN index). Mirrored in one transaction; safe to lose either.
- **`Searcher.VSSTable`** — opt-in fast path. When set and the user has
  no kind/path filter, `SearchSemantic` issues `embedding MATCH ? AND
  k = ?` against vec0 and skips the scan. Falls back softly on any
  query error (e.g. table missing for a changed dim).
- **Config** — `index.vector.extension_path`, `index.vector.auto_create`,
  `index.vector.ef_search` (reserved for HNSW tuning when vec0 ships it).
- **`embed.UnpackInto`** — alloc-free variant of `Unpack` used in the
  brute-force hot loop. Avoids 100k `[]float32` allocations per query
  at 100k-chunk scale.
- **Two-pass brute-force search** — first pass scans only `(id,
  embedding)` columns to find top-k; second pass hydrates the 10
  winners with path/symbol/content. Eliminates ~30× the per-row I/O
  vs v1.3. At 10k chunks this took latency from 166 ms → 114 ms.
- **Semantic-search benchmark matrix** at
  `internal/query/semantic_bench_test.go` — 10k / 50k / 100k / 768 dim
  on brute-force. Numbers published in README.

### Measured

On an Intel i7-1165G7 (Tiger Lake), 768-dim, brute-force fallback, k=10:

| corpus | p50 |
|---|---|
| 10k chunks | ~114 ms |
| 50k chunks | ~555 ms |
| 100k chunks | ~1.10 s |

The plan's aspirational target was <50 ms at 100k via vec0 KNN. That
requires the extension installed; the brute-force path is ~22× slower
at 100k but still correct. The vec0 fast path is architecturally
complete but *untested in this release* — validate on your machine with
the install recipe in README.

### Honest scope note

The vec0 KNN code path in `Searcher.searchViaVSS` is written and
compiles, and the dual-write + extension-loading plumbing is tested on
the fallback path (no extension present in this dev env). We do not
claim measured vec0 numbers until a contributor benchmarks with the
extension loaded.

## [v1.3.0] — 2026-04-22

"TS and Python scope resolvers" — the third v2.0 milestone (Pillar A,
completed for non-Go languages). Brings v0 textual refs up to the
visited-and-stamped floor for TypeScript (`ResolverVersion=2`) and
Python (`ResolverVersion=3`).

### Added

- **`internal/resolver/python`** — stateless per-file resolver. Handles
  `import` / `from-import` bindings (including aliases), `self.method()`
  and `cls.method()` inside classes, module-qualified calls like
  `foo.bar()` via namespace-style imports. Every visited call is stamped
  `ResolverVersion=3` so the SQL short-name fallback skips it.
- **`internal/resolver/typescript`** — same shape for TS/TSX. Named
  imports + aliased imports + default imports + `import * as ns`
  namespace imports all resolve. `this.method()` inside classes resolves
  to the class's own methods. Stamps `ResolverVersion=2`.
- **`pipeline.Resolver` interface** + `Pipeline.Resolvers
  map[string]Resolver` — replaces the per-resolver field pile. Legacy
  `GoResolver` field still honored for backward compatibility.
- **Three new integration-test cases** — `v1.3_ts_this_method_resolution`
  (AuthService.issueToken → this.fingerprint lands as a resolved ref),
  `v1.3_python_self_method_resolution` (JobQueue.drain → self.dequeue),
  `v1.3_no_truly_unresolved_refs` (all TS + Python calls in the fixture
  are visited and stamped).

### Explicit non-goals (stays textual)

- TS: generics, conditional types, declaration merging, ambient modules
  beyond `tsconfig.paths`, arbitrary `obj.method()` that needs type
  inference.
- Python: `super()` chain resolution, `getattr(obj, 'm')(...)` dynamic
  attribute access, type-based method dispatch.

### Fixture additions

- `testdata/fixtures/sample/src/auth.ts` grew `normalizeEmail`,
  `issueToken`, `fingerprint` — together they exercise cross-module
  imports, `this.`-calls, and cross-function linking within a class.
- `testdata/fixtures/sample/py/worker.py` grew `drain` — exercises
  `self.`-calls and param-typed calls we deliberately don't resolve.

### Self-index unchanged

The self-index already hit 0.0% unresolved in v1.2 (pure Go repo).
v1.3 additions keep it there: 66 files, 454 symbols, 2488 refs, 0
resolution-bug self-loops, 0 truly-unresolved non-import refs.

## [Unreleased (v1.2 hotfixes)]

- **`LIMITATIONS.md`** at repo root — single source of truth for what
  doesn't work today, grouped by cause (resolution quality, graph queries,
  indexing/scale, distribution, tooling surface). Linked from README and
  CLAUDE.md. Edit on every milestone.
- **Depth-clamp surfaces a note** — requesting `get_neighborhood` with
  depth > 5 now returns a `notes` entry on the result explaining the
  clamp and pointing at LIMITATIONS.md. Visible in the CLI (stderr),
  HTTP, and MCP responses. Silent clamp was too easy to miss.

## [v1.2.0] — 2026-04-22

"Go, but honest" — the second v2.0 milestone (Pillar A for Go). Type-aware
reference resolution kills the self-loop class of resolution bugs and pushes
the unresolved-ref ratio on mycelium's own repo from 74.8% to 0%.

### Added

- **`internal/resolver/golang`** — Go type resolver built on
  `golang.org/x/tools/go/packages` + `go/types`. Loads the whole module
  once, walks each file's AST using the cached `*types.Info` side tables,
  and rewrites call-ref `DstName` into the same `pkg.Receiver.Method`
  shape the parser uses for its own symbols. Stamps every visited call
  with `ResolverVersion=1` regardless of whether it could rewrite the
  name, so builtins/conversions/erased-receiver calls are correctly
  classified as "analyzed, no local target" rather than "unknown."
- **Migration `0004_resolver_version.sql`** — `refs.resolver_version`
  column + index. 0 = textual, 1 = go-types resolver, 2+ reserved for TS
  (v1.3) / Python (v1.3).
- **Honest metrics** in `query.Stats` — `NonImportRefs`, `RefsTypeResolved`,
  `RefsExternalKnown`, `RefsTrulyUnresolved`, `RecursionSelfLoops`.
  `UnresolvedRatio()` now measures genuine unresolved-ness (v0 + no link,
  non-import), not "dst_symbol_id IS NULL" (which lumped stdlib calls in
  as "failures").
- **`MYCELIUM_RESOLVER_DEBUG=1`** env var — per-file resolution counts on
  stderr for diagnosing edge cases without a rebuild.

### Changed

- SQL resolver's unique-short-name fallback is now **v0-only**. Refs the
  type-aware pass visited skip the ambiguity-prone fallback, eliminating
  the self-loop class (e.g. `ix.db.Close()` no longer resolves to our
  `Index.Close`).
- `self_loop_count` now counts only resolution-bug self-loops (v0);
  genuine recursion (v1) is reported separately as `recursion_self_loops`.
- `Tests: true` in the `packages.Config` — integration and bench test
  files are now part of the type graph.
- Go `go` directive bumped to 1.25.0 (required by `golang.org/x/tools`).

### Self-index baselines (Tiger Lake laptop, `myco doctor`)

| metric | v1.1 | v1.2 |
|---|---|---|
| self_loop_count (bugs) | 11 | **0** |
| recursion_self_loops (informational) | n/a | 12 |
| unresolved_ref_ratio | 74.8% | **0.0%** |
| refs_resolved_local | 556 | 550 |
| refs_external_known | n/a | 1425 |
| doctor exit code | 2 (fail) | 0 (pass) |

### Benchmarks (10k synthetic Go symbols, Tiger Lake)

| op | v1.1 | v1.2 |
|---|---|---|
| initial index | 2433 sym/sec | 2347 sym/sec (−3.5%) |

Note: benchmark fixtures don't carry a `go.mod`, so the resolver is nil in
this measurement. The resolver adds a fixed one-time cost per Pipeline
construction for the `packages.Load` call (~200ms on the self-index).

## [v1.1.0] — 2026-04-22

First milestone on the v2.0 roadmap ("Honest signals"). Adds health checks
so later milestones can measure themselves against honest baselines.

### Added

- **`myco doctor`** subcommand with per-check Pass/Warn/Fail output and
  conventional exit codes (0/1/2). `--json` flag for CI.
- **`internal/doctor`** package — configurable thresholds, pluggable into
  future MCP introspection.
- **Extended `stats`** — `self_loop_count`, `unresolved_by_language`,
  `total_refs_by_language`, `stale_chunks`, `embed_queue_depth`, DB size and
  fragmentation, plus `UnresolvedRatio()` / `DBFragmentation()` helpers.
- **Benchmark harness** — `GenerateSyntheticRepo()` emits deterministic
  Go-only fixtures at arbitrary symbol counts. Benchmarks for initial index,
  `FindSymbol`, and `GetNeighborhood` depth-2. Baselines at 10k symbols on
  a Tiger Lake laptop: **2433 sym/sec**, **11.4 ms** point lookup, **3.8 ms**
  neighborhood query.

### Baselines captured

Self-index of mycelium under provider=none:

- 57 files · 387 symbols · 2045 refs
- self_loop_count: **11** (Pillar A in v1.2 targets 0)
- unresolved_ref_ratio: **72.8%** (Pillar A target <8% for Go)
- db_fragmentation: 11.1%

## [v1.0.0] — 2026-04-22

First stable release. Nine MCP tools, three transports, three languages.

### Added

- **Release binaries.** GitHub Actions matrix build for `linux/amd64`,
  `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`. Version
  injected via `-ldflags "-X main.version=…"`.
- **Integration test.** Committed multi-language fixture
  (`testdata/fixtures/sample`) exercised end-to-end in CI: parsers, index,
  all nine query methods.
- **CI.** Lint + vet + race-enabled tests on Linux and macOS.

## [v0.5.0] — 2026-04-21

### Added

- **`search_lexical`** — parallel 4-worker ripgrep-style regex scan over
  indexed files. Fills the gap where semantic search misses exact strings.
- **`get_file_summary`** — structural summary per file: exports, imports,
  LOC, symbol counts by kind. No LLM calls.
- **`get_neighborhood`** — local call graph around a symbol via recursive
  CTE on `refs`. Depth capped at 5; direction = out | in | both.
- **HTTP transport** — loopback server on `127.0.0.1:<http_port>`. Routes:
  `POST /rpc` with `{method, params}` and per-method `POST /<method>`.
- **Parallel initial scan** — worker pool for parsing; single-writer
  goroutine for DB commits. Threshold-gated (≥200 files) to avoid
  goroutine overhead on small repos.

## [v0.4.0] — 2026-04-21

### Added

- **Semantic search** (`search_semantic`) — embeds the query, brute-force
  cosine similarity over stored float32 vectors. Top-k with snippet,
  kind/path filtering.
- **Embedders.** `Noop` (default), `Ollama` (local `http://localhost:11434`),
  `Fake` (test-only). Pluggable via `.mycelium.yml`.
- **Chunker.** One chunk per symbol with qualified name + signature +
  docstring + body; skips tiny const/var without docstrings.
- **Embed queue + worker.** Background goroutine in the daemon; batches to
  the embedder, writes to `chunks.embedding` + `embed_cache`. Rate-limit
  circuit breaker (trailing 60s).
- **Model-switch invalidation.** Changing `embedder.model` on daemon start
  drops stale vectors automatically.

### Changed

- Migrated chunks table to include `content`, `embedding`, `embed_model`
  columns (migration `0002_embeddings.sql`). Deferred `sqlite-vec` —
  brute-force Go cosine is fast enough for typical repos.

## [v0.3.0] — 2026-04-21

### Added

- **MCP stdio server** (`myco mcp`) — minimal JSON-RPC 2.0 over stdio, no
  external MCP SDK. Exposes five tools: `find_symbol`, `get_references`,
  `list_files`, `get_file_outline`, `stats`.
- **`myco init`** — writes `.mycelium.yml`, adds `.mycelium/` to
  `.gitignore`, installs post-commit hook, prints Claude Code / Cursor MCP
  config snippet via `--mcp claude|cursor`.
- **Post-commit git hook** — reconciles the index after commits when the
  daemon isn't running.
- **TypeScript/TSX parser** — `smacker/go-tree-sitter` grammar; extracts
  function / class / interface / type / enum / var / method / field decls
  plus import + call refs. Leading `_` heuristic for private.
- **Python parser** — tree-sitter grammar; extracts function / class /
  method decls with PEP-257 docstring detection. `_`-prefix convention for
  private; dunders are public.
- **Shared tree-sitter helpers** (`internal/parser/tsutil`) — slice, position,
  walk, preceding-comment extraction.

## [v0.2.0] — 2026-04-21

### Added

- **Daemon** (`myco daemon`) — long-running per-repo process that owns the
  index. Thin clients (CLI, MCP, hook, HTTP later) talk to it via a unix
  socket at `.mycelium/daemon.sock`.
- **fsnotify watcher** — recursive watch with per-file debounce window;
  auto-registers new directories.
- **Reference resolution pass.** Two-step: exact qualified match, then
  unique short-name match via `refs.dst_short` column. `ON DELETE SET NULL`
  cascades keep refs honest.
- **`get_references`, `list_files`, `get_file_outline`** query methods.
  Refs flag each hit as `resolved` vs `textual`.
- **Query package** (`internal/query`) — the single reader of the DB.
  All transports call this package.

## [v0.1.0] — 2026-04-21

Initial indexer. Go-only. One-shot CLI.

### Added

- **Go parser** — stdlib `go/ast`, no cgo. Extracts functions, methods,
  types (struct / interface / alias), top-level vars / consts, imports,
  call-site refs.
- **SQLite schema** (`migrations/0001_init.sql`) — files, symbols, refs,
  chunks, `symbols_fts` (FTS5 trigram), `embed_cache`, `embed_queue`, meta.
- **Walker** (`internal/repo`) — doublestar-matching include/exclude, size
  limits, `.git` / `.mycelium` skipping.
- **One-shot pipeline** — hash-gated per-file transactions.
- **`myco index`, `myco query find`, `myco stats`** subcommands.
