# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project adheres
to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [v2.0.0-rc1] — 2026-04-24

First release candidate for v2.0 ("precision and scale"). No new
functional changes since v1.7; this tag consolidates the v1.1 → v1.7
series into a single release and gates the remaining v2.0 work.
Per-milestone details remain in the sections below.

### Delivered against the v2.0 acceptance criteria

- **Type-aware references** for Go, TypeScript, Python. Self-index
  reports `self_loop_count = 0`, `unresolved_ref_ratio = 0.0%`.
  (v1.2, v1.3)
- **Workspace mode**: one daemon, one SQLite, N sub-projects with
  per-project config and optional `project` filter on every query
  tool. (v1.5)
- **Graph-native tools**: `impact_analysis`, `critical_path`. (v1.6)
- **PR-scoped queries**: `--since <ref>` on five read methods. (v1.6)
- **Doctor + quality signals**: `myco doctor` exits 0/1/2 on
  pass/warn/fail with configurable thresholds. (v1.1, v1.2, v1.7)
- **Watchman opt-in** behind `watcher.backend`. (v1.7)
- **sqlite-vec integration** compiled in; brute-force fallback
  measured. (v1.4)

Architectural invariants from v1.0 are preserved: SQLite is still
source of truth and query engine; `internal/query` is the sole
reader; `internal/pipeline` is the sole writer; no new top-level
processes; all schema changes are additive.

### Known gaps before the final v2.0 tag

- **sqlite-vec p95 unmeasured.** The vec0 code path compiles on
  every release target but the "p95 < 50ms at 100k chunks"
  benchmark from the roadmap has not been run on a machine with
  the extension installed. Brute-force numbers on Tiger Lake
  (768 dims): 114ms / 555ms / 1100ms at 10k / 50k / 100k.
- **`libsqlite_vec.{so,dylib,dll}` not bundled** in the release
  tarball. Users install `sqlite-vec` manually per the README.
- **No 100k+ file monorepo validation.** `myco doctor`,
  workspace mode, and the inotify-headroom check have only been
  exercised against the self-index and the committed fixtures.

## [v1.7.0] — 2026-04-24

"Watchman opt-in" — the seventh v2.0 milestone (Pillar G). Pluggable
watcher backend so users on 100k+ file repos can escape the
`fs.inotify.max_user_watches` ceiling without changing anything
else about how mycelium runs.

### Added

- **`internal/watch/watchman/`** — minimal in-tree watchman client.
  Talks JSON-over-unix-socket: `get-sockname`, `watch-project`,
  `subscribe`, `unsubscribe`. Read pump demultiplexes command
  responses vs subscription deliveries so one connection handles
  both. `$MYCELIUM_WATCHMAN_SOCK` overrides sockname discovery for
  container setups.
- **Watcher backend selection.** New `watcher.backend` config field
  (`"fsnotify"` default, `"watchman"` opt-in) plus
  `myco daemon --watcher-backend <name>` CLI override. Unknown
  values are a hard error; watchman unavailability falls back to
  fsnotify with a stderr warning so the daemon still starts.
- **`internal/watch` restructure.** Old monolithic `watch.go` split
  into `watcher.go` (public `Watcher` interface + `Options`),
  `common.go` (shared debounce/coalesce/filter wrapper), and
  per-backend sources: `fsnotify.go`, `watchman.go`. Both backends
  route through the same wrapper so behavior is identical — the
  two honest-surface bugs the old struct had (unused
  `MaxFileSizeKB`, unused `CoalesceMS`) are fixed once, not twice.
- **`CoalesceMS`** is now wired. Bursts of debounced events within
  a coalesce window flush as one batch to the output channel.
- **Doctor: `inotify_headroom` check.** Linux-only. Counts repo
  directories vs `/proc/sys/fs/inotify/max_user_watches` and warns
  above 50%, fails above 90%. The warn message suggests either
  switching to `watcher.backend: watchman` or raising the sysctl.

### Changed

- `watch.New` signature went from positional args to an `Options`
  struct (source-incompatible; migrates cleanly — all call-sites
  updated).
- `daemon.Daemon.Watcher` is now `watch.Watcher` (interface) rather
  than `*watch.Watcher` (struct pointer), matching the new backend
  split.

### Fixed

- Shutdown race in the watcher's shared wrapper: coalesce/debounce
  timers could fire `w.send` after the output channel closed. Pump
  now owns every write to `out`; timers signal through internal
  channels. `go test -race ./internal/watch/...` confirms.

## [v1.6.0] — 2026-04-24

"Graph-native tools + PR scope" — the sixth v2.0 milestone (Pillars E
+ F). Two new graph traversals that become cheap once v1.2/v1.3's
type-aware resolvers landed, plus a `--since <ref>` path filter on the
existing read surface for PR-scoped queries.

### Added

- **`impact_analysis(symbol)`** — new MCP tool and CLI `myco query
  impact`. Returns the transitive inbound closure around a symbol as
  a flat list ranked by distance (1 = direct caller). Optional `kind`
  filter narrows the reported set (typical use: `kind=method` to find
  test methods covering the target). Default depth 5, hard ceiling
  10. Composes with `project` and `since` — they scope the *reported*
  callers, not the walk, so cross-file / cross-project chains still
  surface.
- **`critical_path(from, to)`** — new MCP tool and CLI `myco query
  path`. Returns up to `k` shortest outbound call paths. Bounded BFS
  at depth ≤ 8 via a single recursive CTE; cycles prevented by the
  SQLite `instr()` idiom on a comma-delimited accumulated path
  column. Hydrates the distinct vertices in one second-pass query to
  avoid the N+1 fan-out. Default k = 5.
- **`--since <ref>` filter** on `find_symbol`, `get_references`,
  `list_files`, `search_lexical`, `search_semantic`. Resolved via
  `git -C <root> diff --name-only <ref>...HEAD` at the transport
  boundary (daemon RPC handler and CLI offline fallback), then passed
  to the reader as `pathsIn []string`. Three-dot form uses the merge-
  base so "files on my branch" stays correct after the base advances.
- **`internal/gitref/`** — thin helper (`ResolveSince`) that runs the
  `git diff` with a 5s timeout and surfaces stderr verbatim on
  failure. Returns a non-nil empty slice when the ref has no diff
  against HEAD so the reader's zero-row sentinel distinguishes "no
  changes" from "no filter."
- **`internal/query/graph.go`** — `ImpactAnalysis`, `CriticalPath`,
  `ImpactHit`, `Impact`, `PathVertex`, `CriticalPathResult`. Reuses
  `resolveSeed` and `loadNode` from `neighborhood.go`.
- **`internal/query/paths.go`** — shared `pathsInClause` splicer
  renders the `AND f.path IN (?, ?, ...)` WHERE fragment used across
  the five filtered methods. Caps the path list at **500 entries**
  (SQLite's 999-parameter limit) and returns a clear error when a PR
  diff expands beyond that — the correct fix is a tighter base ref.
- **Reader signature change** (additive, source-incompatible) — five
  methods gained a final `pathsIn []string` argument:
  - `FindSymbol(ctx, name, kind, project, limit, pathsIn)`
  - `GetReferences(ctx, target, project, limit, pathsIn)`
  - `ListFiles(ctx, language, nameContains, project, limit, pathsIn)`
  - `SearchLexical(ctx, pattern, pathContains, project, k, repoRoot, pathsIn)`
  - `Searcher.SearchSemantic(ctx, query, k, kind, pathContains, project, pathsIn)`

  `pathsIn = nil` is "unscoped"; `pathsIn = []string{}` is an
  explicit zero-row sentinel. Existing callers pass `nil` to preserve
  prior behavior. An options-struct refactor was considered and
  rejected for mid-release API churn.
- **MCP tool schemas** — two new tool entries (`impact_analysis`,
  `critical_path`), plus a `since` input on `find_symbol`,
  `get_references`, `list_files`, `search_lexical`,
  `search_semantic`. MCP server dispatch in `internal/mcp/server.go`
  routes the two new tools.
- **CLI subcommands** — `myco query impact <symbol>` and `myco query
  path <from> <to>`. `--since <ref>` added to `find`, `refs`, `files`,
  `grep`, `search`. Offline fallback path runs `gitref.ResolveSince`
  locally so `--since` works even without the daemon.
- **Integration tests** at `graph_integration_test.go`:
  - `TestIntegration_ImpactAnalysis` — seeds on `auth.normalizeEmail`
    and asserts `auth.AuthService.fingerprint` at distance 1 and
    `auth.AuthService.issueToken` at distance 2. Subtests for the
    kind-filter narrowing and the depth-clamp note.
  - `TestIntegration_CriticalPath` — asserts the path `issueToken →
    fingerprint → normalizeEmail` surfaces.
  - `TestIntegration_PathsInFilter` — exercises the reader-level
    filter (no git process) across three cases: matching file,
    non-matching file, empty-slice sentinel.
- **`internal/gitref/resolve_test.go`** — temp-git-repo tests covering
  the happy path (two-commit diff), empty ref (error), unknown ref
  (error), and the no-changes case (non-nil empty slice).

### Notes

- `vec0` KNN fast path is skipped when `search_semantic` is called
  with a `project` filter (v1.5) **or** a `since` filter (v1.6) —
  `vec0 MATCH` doesn't compose with arbitrary `WHERE` clauses.
  Brute-force cosine handles scoped semantic search.
- `impact_analysis` is intentionally not a superset of
  `get_neighborhood(direction=in)`. The shapes serve different
  workflows: graph (nodes + edges) vs. flat distance-ranked list; 2
  vs. 5 default depth; 5 vs. 10 max; no kind filter vs. yes.
- Cross-repo federation (N worktrees, one graph) remains a v3
  non-goal.

### Verification

Integration suite green on `TestIntegration_IndexAndQuery`,
`TestIntegration_WorkspaceMode`, `TestIntegration_ImpactAnalysis`,
`TestIntegration_CriticalPath`, `TestIntegration_PathsInFilter`, plus
all four `internal/gitref` cases. `go vet -tags sqlite_fts5 ./...`
clean. No schema changes, no migration.

## [v1.5.0] — 2026-04-23

"Workspace mode" — the fifth v2.0 milestone (Pillar C). One daemon, one
SQLite, N sub-projects under one worktree. Not cross-repo federation
(that's v3): the unit of isolation is a directory inside the same repo,
each with its own `languages` / `include` / `exclude` overrides.

### Added

- **Migration `0005_projects.sql`** — new `projects(id, name, root,
  created_at)` table plus `files.project_id` FK with cascade delete. A
  NULL `project_id` means the file belongs to the implicit root project
  (v1.4 configs keep working untouched).
- **`config.ProjectConfig`** — optional `projects:` list in
  `.mycelium.yml`. Each entry has `name`, `root`, and optional
  `languages`/`include`/`exclude` overrides. Embedder/chunking stay
  inherited from the top level (one DB can't mix embedding dims).
- **`internal/index/projects.go`** — `UpsertProject`, `PruneProjects`,
  `ListProjects`. Idempotent upsert by name; prune drops rows no longer
  in config (cascades remove their files + symbols + refs + chunks).
- **`pipeline.Workspace`** — per-project walker + project_id. The
  pipeline now accepts a `Workspaces []Workspace` slice; each walker
  runs with its own roots/filters and every file it emits is tagged
  with the owning project before hitting the writer. Legacy single-
  `Walker` mode still works when `Workspaces` is empty.
- **`Pipeline.FileProjectFor`** — longest-prefix resolver so fsnotify
  events from the watcher can attribute a changed file back to its
  project on the single-file update path.
- **Query-side `project` parameter** — `FindSymbol`, `GetReferences`,
  `ListFiles`, `SearchLexical`, `SearchSemantic`, `GetNeighborhood`
  each accept an optional project name. A splicer (`projectScope`) adds
  `AND f.project_id = ?` when set; unknown project names return zero
  hits rather than silently falling back to unscoped (config bug
  visibility). For `GetNeighborhood`, only the seed lookup is scoped —
  traversal stays global so cross-project call graphs surface.
- **IPC + MCP + CLI plumbing** — `Project` field added to every
  params struct that touches files. MCP tool schemas advertise the
  optional `project` input. CLI gains `--project <name>` on `myco query
  find | refs | files | grep | search | neighbors`.
- **Workspace integration test + fixture** at
  `testdata/fixtures/workspace` (3 sub-projects: Go `api`, TS `web`,
  Python `worker`) in `workspace_integration_test.go`. Verifies
  per-project scoping on `find_symbol` and `list_files`, the
  unknown-project zero-hit contract, and that every indexed file has a
  non-null `project_id` pointing at the right row.

### Notes

- The vec0 fast path is skipped when a project filter is active — vec0
  MATCH doesn't compose with arbitrary WHERE clauses. Brute-force
  cosine handles project-scoped semantic search.
- Embedder inheritance is intentional: a single SQLite DB can't mix
  embedding dimensions cleanly, so per-project embedder overrides are
  deliberately out of scope.

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
