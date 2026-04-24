<p align="center">
  <img src="./assets/myco.png" width="280" alt="Mycelium Logo">
</p>

<h1 align="center">Mycelium</h1>

<p align="center">
  <strong>A local, always-fresh repository knowledge base for AI coding agents.</strong><br>
  <em>Stop wasting tokens. Give your agents instant, structural, and semantic context.</em>
</p>

<p align="center">
  <a href="https://github.com/Datata1/mycelium/releases"><img src="https://img.shields.io/github/v/release/Datata1/mycelium?style=flat-square&color=00FFCC" alt="Release"></a>
  <a href="#"><img src="https://img.shields.io/badge/Storage-SQLite-blue?style=flat-square" alt="SQLite"></a>
  <a href="#"><img src="https://img.shields.io/badge/Interface-MCP%20%7C%20HTTP%20%7C%20CLI-8A2BE2?style=flat-square" alt="Interfaces"></a>
  <a href="#"><img src="https://img.shields.io/badge/Status-v1.6%20Graph%20%26%20PR%20Scope-success?style=flat-square" alt="Status"></a>
</p>

---

> **Binary:** `myco` &nbsp;&middot;&nbsp; **Storage:** SQLite at `.mycelium/index.db` &nbsp;&middot;&nbsp; **Interfaces:** MCP, HTTP, CLI

Mycelium parses your repo with Go/AST and tree-sitter, stores symbols, references, and (optionally) semantic embeddings in a single SQLite file, and serves that index to **Claude Code, Cursor, and any other MCP client.** A background daemon keeps the index within a few hundred milliseconds of what's on disk. **No external services, no Docker.**

See [CONTEXT.md](./CONTEXT.md) for the problem, goals, and non-goals, [CHANGELOG.md](./CHANGELOG.md) for version history, and [LIMITATIONS.md](./LIMITATIONS.md) for the honest list of what doesn't work yet and why.

---

## ⚡ Why Mycelium?

AI coding agents waste tokens and tool calls re-discovering a repo's structure on every single task. 
* **Ripgrep** is blind to structure.
* **Full-text search** misses semantics.
* **Whole-file reads** blow out your context windows. 

**Mycelium** gives agents a structured, always-fresh index they can query precisely.

## Status

**v1.6** — graph-native tools + PR scope. Two new MCP tools
(`impact_analysis`, `critical_path`) over the now-honest ref graph,
plus a `--since <ref>` path filter on the read surface for PR-scoped
queries. Builds on v1.5's workspace mode, v1.2/v1.3 type-aware
resolvers for Go/TS/Python, and v1.4's optional sqlite-vec fast path.
Cross-repo federation stays a v3 non-goal (see
[LIMITATIONS.md](./LIMITATIONS.md)).

## Install

### From release binaries (recommended)

Grab the tarball for your platform from
[GitHub Releases](https://github.com/jdwiederstein/mycelium/releases):

```bash
# Linux amd64
curl -sSL https://github.com/jdwiederstein/mycelium/releases/latest/download/myco-linux-amd64.tar.gz \
  | tar -xz -C /tmp && sudo mv /tmp/myco-linux-amd64 /usr/local/bin/myco

# macOS arm64 (Apple Silicon)
curl -sSL https://github.com/jdwiederstein/mycelium/releases/latest/download/myco-darwin-arm64.tar.gz \
  | tar -xz -C /tmp && sudo mv /tmp/myco-darwin-arm64 /usr/local/bin/myco
```

Supported platforms: `linux/amd64`, `linux/arm64`, `darwin/amd64`,
`darwin/arm64`, `windows/amd64`.

### From source

Requires Go 1.25+ (for `golang.org/x/tools`) and a C toolchain (tree-sitter uses cgo).

```bash
git clone https://github.com/jdwiederstein/mycelium
cd mycelium
go build -tags sqlite_fts5 -o /usr/local/bin/myco ./cmd/myco
```

**The `sqlite_fts5` build tag is required** — it enables FTS5 in the
embedded SQLite driver, which we use for fuzzy symbol search.

## Quick start

```bash
cd my-repo
myco init                 # writes .mycelium.yml, installs post-commit hook
myco daemon &             # start the watcher + index server
myco query find AuthService
myco query grep "TODO"
myco query neighbors ParseRequest --direction in --depth 2
```

### Wiring into Claude Code

```bash
myco init --mcp claude
```

This prints a JSON snippet for `~/.claude.json` that tells Claude Code how to
spawn the MCP server. Paste it in, restart Claude Code, and the nine tools
appear automatically.

### Wiring into Cursor

```bash
myco init --mcp cursor
```

Same idea, but for `~/.cursor/mcp.json`.

## MCP tools exposed

All tools return JSON; line/col positions are 1-based.

Tools marked with `project?` accept an optional workspace project name
(see [Workspace mode](#workspace-mode)). Unknown project names return
zero hits rather than silently falling back to unscoped, so config typos
surface immediately. Tools marked with `since?` accept a git ref and
restrict results to files changed between `<ref>...HEAD` (see
[PR-scoped queries](#pr-scoped-queries)).

| Tool | Purpose | Key inputs |
|---|---|---|
| `find_symbol` | Fuzzy/exact symbol lookup. | `name`, `kind?`, `limit?`, `project?`, `since?` |
| `get_definition` | Source span + snippet. | `symbol_id` or `qualified_name` |
| `get_references` | Callers / importers / type uses; flags each hit as resolved vs textual. | `target`, `limit?`, `project?`, `since?` |
| `search_semantic` | Vector search over code chunks (requires embedder). | `query`, `k?`, `kind?`, `path_contains?`, `project?`, `since?` |
| `search_lexical` | Ripgrep-style regex over indexed files. | `pattern`, `path_contains?`, `k?`, `project?`, `since?` |
| `list_files` | Indexed files with language tags. | `language?`, `name_contains?`, `limit?`, `project?`, `since?` |
| `get_file_outline` | Hierarchical symbol tree for one file. | `path` |
| `get_file_summary` | Structural summary (exports, imports, LOC). | `path` |
| `get_neighborhood` | Local call graph around a symbol (recursive CTE on refs). Seed lookup respects `project?`; traversal stays global so cross-project edges still surface. | `target`, `depth?`, `direction?`, `project?` |
| `impact_analysis` | Transitive inbound closure ranked by distance (v1.6). For "who's impacted if I change this?" or (with `kind=method`) "what tests cover this?". | `target`, `kind?`, `depth?`, `project?`, `since?` |
| `critical_path` | Up to `k` shortest outbound call paths from `from` to `to` (v1.6). Bounded BFS at depth ≤ 8. | `from`, `to`, `depth?`, `k?`, `project?` |
| `stats` | Languages, symbol counts, refs, freshness. | — |

## Architecture

```
                           .mycelium.yml
                                |
                                v
    +-------------+   spawn   +-----------------+
    | Claude Code |---------->| myco mcp        |  stdio MCP; short-lived
    | / Cursor    |<----------| (thin client)   |
    +-------------+           +-----------------+
                                        |
                                        | unix socket
                                        v
                           +-----------------------------+
                           |   myco daemon               |
                           |  watcher -> queue ->        |
                           |  parser -> diff -> writer   |
                           |    + embed worker           |
                           +-----------------------------+
                                        ^
                                        | HTTP :7777 (loopback)
                                        v
                            +-----------------+
                            | myco CLI / curl |
                            +-----------------+
```

One writer, many readers. The daemon owns SQLite; `internal/query` is the
sole reader; MCP, HTTP, and CLI are thin transports over the same dispatcher.

## Configuration

Edit `.mycelium.yml` in your repo root (generated by `myco init`):

```yaml
version: 1
languages: [go, typescript, python]
include:
  - "**/*.go"
  - "src/**/*.{ts,tsx}"
  - "**/*.py"
exclude:
  - "**/node_modules/**"
  - "**/vendor/**"
embedder:
  provider: none              # none | ollama | voyage | openai
  # model: nomic-embed-text
  # dimension: 768
  # endpoint: http://localhost:11434
  batch_size: 16
  max_concurrency: 2
  rate_limit_chunks_per_minute: 2000
chunking:
  symbol_max_tokens: 1024
  include_docstrings: true
  file_fallback_window_lines: 50
watcher:
  debounce_ms: 200
  coalesce_ms: 2000
daemon:
  socket: .mycelium/daemon.sock
  http_port: 7777             # 0 to disable
hooks:
  post_commit: true
index:
  path: .mycelium/index.db
  max_file_size_kb: 1024
```

## Workspace mode

Monorepos with independent sub-projects (e.g. a Go API next to a TS
frontend and a Python worker) can register each sub-project separately
under one daemon. One SQLite file, one watcher, N logical scopes.

```yaml
# .mycelium.yml — top-level languages/include/exclude remain the
# defaults; each entry under `projects:` can override them.
projects:
  - name: api
    root: services/api
    languages: [go]
    include: ["**/*.go"]
  - name: web
    root: services/web
    languages: [typescript]
    include: ["**/*.ts", "**/*.tsx"]
  - name: worker
    root: services/worker
    languages: [python]
    include: ["**/*.py"]
```

Every query tool gains an optional `project` input (and `--project` on
the CLI) that scopes results to one sub-project. `get_neighborhood`
scopes only the seed lookup — traversal stays global so cross-project
call edges still surface.

```bash
myco query find Handler --project api
myco query files --project web
```

**Scope.** Workspace mode is about isolating sub-projects *inside one
worktree*. Cross-repo federation (N worktrees sharing one logical graph)
is an explicit v3 non-goal — the unit of isolation here is a directory,
not a repository. The embedder is inherited from the top level because a
single SQLite DB can't mix embedding dimensions.

**Backwards compatibility.** Configs without a `projects:` list keep
working untouched; `project_id` on the `files` table is nullable and
NULL means "implicit root project."

## PR-scoped queries

Reviewing a pull request? Pass `--since <ref>` (CLI) or `since` (MCP)
to restrict any read to files changed between `<ref>...HEAD`. The
daemon resolves this with `git diff --name-only` at request time — no
caching, no schema change, just an additional `AND path IN (...)`
clause on the query.

```bash
# Everything changed since main, scoped to this feature branch
myco query files --since main
myco query find Handler --since main
myco query grep "TODO" --since origin/main
myco query impact MyService --since HEAD~5 --kind method   # tests for recent changes
```

The three-dot form `<ref>...HEAD` is what `git` interprets under the
hood — it asks for the symmetric diff against the merge-base so
"files on my branch" stays correct after the base advances.

Hard cap: PR diffs expanding to **>500 files** error out with a
pointer to pick a tighter base ref. That's SQLite's 999-parameter
limit biting first; at that scale the filter isn't carrying its
weight anyway. `impact_analysis` and the five filtered read tools
compose `since` with `project` naturally — both are additive `AND`
clauses.

### New graph tools

Two traversals over the (now honest, post-v1.3) ref graph:

```bash
# Who's transitively impacted if I change this function?
myco query impact AuthService.issueToken --depth 5

# Only show test methods that cover it:
myco query impact AuthService.issueToken --kind method

# Is there a dependency chain between these two?
myco query path cmd.main auth.normalizeEmail --k 3
```

- **`impact_analysis`** returns a flat list ranked by shortest distance
  (1 = direct caller). Default depth 5, max 10. For the full graph
  shape use `get_neighborhood` instead.
- **`critical_path`** returns up to `k` shortest outbound paths,
  bounded at depth ≤ 8. Cycles are rejected inside the SQL CTE via
  `instr()` on the accumulated path column, so dense graphs don't
  explode.

Both compose with `project?`; `critical_path` scopes only the two seed
lookups so paths through another project still surface.
`impact_analysis` additionally respects `since?` so you can ask "who's
affected in the files this PR touched?".

## Enabling semantic search

Semantic search requires an embedder. The simplest path is Ollama.

```bash
# 1. Install Ollama: https://ollama.com
ollama pull nomic-embed-text

# 2. Edit .mycelium.yml:
#    embedder:
#      provider: ollama
#      model: nomic-embed-text
#      dimension: 768

# 3. Restart the daemon — the worker embeds everything in the background.
```

Without an embedder configured, `search_semantic` cleanly returns
`embeddings_not_configured`. All other tools work unaffected.

### Scaling with sqlite-vec (v1.4+)

The default brute-force cosine search works but scans every embedding per
query — fine below ~10k chunks, painful above. For larger repos install
the [sqlite-vec](https://github.com/asg017/sqlite-vec) extension and point
the config at it:

```bash
# Install: pick the right prebuilt .so/.dylib/.dll for your platform from
#   https://github.com/asg017/sqlite-vec/releases
# Example on Linux amd64:
mkdir -p /usr/local/lib/mycelium
cp vec0.so /usr/local/lib/mycelium/vec0.so

# Edit .mycelium.yml:
#   index:
#     vector:
#       extension_path: /usr/local/lib/mycelium/vec0.so
#       auto_create: true

# Restart the daemon — `vss_chunks` is created and backfilled on first run.
```

When the extension loads cleanly the daemon creates a `vss_chunks` virtual
table, mirrors existing embeddings into it, and routes `search_semantic`
through vec0's KNN path. If the extension is missing or mismatched, the
daemon logs a warning and falls back to brute-force — your queries still
work, just at the numbers below.

Note: the vec0 fast path is skipped when `search_semantic` is called with
a `project` filter. vec0's `MATCH` operator doesn't compose with arbitrary
`WHERE` clauses, so project-scoped semantic search falls back to brute-force
cosine over that project's chunks. Unfiltered queries keep the fast path.

### Benchmark — brute-force fallback

Measured on an Intel i7-1165G7 laptop (Tiger Lake), 768-dim embeddings,
random unit-norm vectors, `k=10`, cold cache avoided:

| corpus | brute-force p50 |
|---|---|
| 10k chunks | ~114 ms |
| 50k chunks | ~555 ms |
| 100k chunks | ~1.10 s |

Scaling is linear at about 11 µs per chunk per query. Above 50k chunks,
install sqlite-vec. The full benchmark lives in
`internal/query/semantic_bench_test.go` — run with:

```bash
go test -tags sqlite_fts5 -run=^$ \
  -bench=BenchmarkSemanticSearch -benchtime=5x -count=3 \
  ./internal/query/
```

## CLI reference

```bash
myco init [--mcp claude|cursor]       # set up in the current repo
myco daemon                           # run the watcher + server
myco mcp                              # MCP stdio (spawned by agents)
myco index                            # one-shot full reindex (debug / CI)
myco stats                            # index freshness + counts

myco query find <name> [--kind K] [--limit N] [--project P] [--since REF]
myco query refs <symbol> [--limit N] [--project P] [--since REF]
myco query files [name-contains] [--language L] [--limit N] [--project P] [--since REF]
myco query outline <path>
myco query summary <path>
myco query neighbors <symbol> [--depth N] [--direction out|in|both] [--project P]
myco query impact <symbol> [--kind K] [--depth N] [--project P] [--since REF]
myco query path <from> <to> [--depth N] [--k N] [--project P]
myco query grep <regex> [--path P] [--k N] [--project P] [--since REF]
myco query search <text> [--k N] [--kind K] [--path P] [--project P] [--since REF]

myco hook post-commit                 # run by .git/hooks/post-commit
```

## HTTP API

The daemon also exposes the same dispatcher over HTTP on `127.0.0.1:<http_port>`.
Two route shapes:

```bash
# Single /rpc endpoint — same shape as the unix socket protocol.
curl -s -X POST -d '{"method":"find_symbol","params":{"name":"Pipeline"}}' \
  http://127.0.0.1:7777/rpc

# Per-method routes — method inferred from path.
curl -s -X POST -d '{"name":"Pipeline"}' http://127.0.0.1:7777/find_symbol
```

## Development

```bash
# Build + run locally
go build -tags sqlite_fts5 -o /tmp/myco ./cmd/myco
/tmp/myco index

# Tests
go test -tags sqlite_fts5 -race ./...

# Integration test (multi-language fixture under testdata/fixtures/sample)
go test -tags sqlite_fts5 -run TestIntegration ./...
```

## Troubleshooting

**`no such module: fts5`** — rebuild with the `sqlite_fts5` build tag:

```bash
go build -tags sqlite_fts5 -o /tmp/myco ./cmd/myco
```

**Daemon won't start: `listen: address already in use`** — a previous daemon
didn't clean up. The socket file lives at `.mycelium/daemon.sock`; delete
it and retry. HTTP port conflicts: set `daemon.http_port: 0` in config.

**Semantic search returns `embeddings_not_configured`** — the embedder is
set to `none` in `.mycelium.yml`. Switch to `ollama` or a hosted provider;
see "Enabling semantic search" above.

**Refs resolution creates self-loops.** Known limitation: when a method name
is unique in the repo, selector calls like `ix.db.Close()` resolve to our
`Index.Close` because we don't have type information. Type-aware resolution
is deferred; the self-loop is visible but doesn't break other queries.

**fsnotify misses edits on save in vim/VSCode.** Some editors atomic-rename
on save, delivering CREATE+DELETE instead of MODIFY. The `embed_cache` keyed
by content hash makes this a no-op for embeddings; for the index itself,
the post-commit hook acts as a catch-up.

## Contributing

See [CONTEXT.md](./CONTEXT.md) for the architectural rules — in particular,
the "one writer, one reader" boundary, the no-cgo-in-Go-parser rule, and
why semantic search defaults to off.

## License

TBD. The repository is currently unlicensed; a license will be chosen before
the first tagged release.
