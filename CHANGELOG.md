# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project adheres
to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
