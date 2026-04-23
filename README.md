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
  <a href="#"><img src="https://img.shields.io/badge/Status-v1.0%20Feature%20Complete-success?style=flat-square" alt="Status"></a>
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

**v1.0** — feature-complete. Nine MCP tools, three transports, three
languages (Go, TypeScript/TSX, Python).

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

Requires Go 1.22+ and a C toolchain (tree-sitter uses cgo).

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

| Tool | Purpose | Key inputs |
|---|---|---|
| `find_symbol` | Fuzzy/exact symbol lookup. | `name`, `kind?`, `limit?` |
| `get_definition` | Source span + snippet. | `symbol_id` or `qualified_name` |
| `get_references` | Callers / importers / type uses; flags each hit as resolved vs textual. | `target`, `limit?` |
| `search_semantic` | Vector search over code chunks (requires embedder). | `query`, `k?`, `kind?`, `path_contains?` |
| `search_lexical` | Ripgrep-style regex over indexed files. | `pattern`, `path_contains?`, `k?` |
| `list_files` | Indexed files with language tags. | `language?`, `name_contains?`, `limit?` |
| `get_file_outline` | Hierarchical symbol tree for one file. | `path` |
| `get_file_summary` | Structural summary (exports, imports, LOC). | `path` |
| `get_neighborhood` | Local call graph around a symbol (recursive CTE on refs). | `target`, `depth?`, `direction?` |
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

myco query find <name> [--kind K] [--limit N]
myco query refs <symbol> [--limit N]
myco query files [name-contains] [--language L] [--limit N]
myco query outline <path>
myco query summary <path>
myco query neighbors <symbol> [--depth N] [--direction out|in|both]
myco query grep <regex> [--path P] [--k N]
myco query search <text> [--k N] [--kind K] [--path P]

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
