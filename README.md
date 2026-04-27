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

AI coding agents already know how to use `Read`, `Glob`, and `Bash`.
They have to be *told* to use a new MCP tool, and even then their
reflex on the next task is still `grep -rn`. v3 of mycelium is
designed around that reality: it gives the agent two surfaces to
reach for, both of which feel like the tools it already trusts.

- **A browseable filesystem at `.mycelium/skills/`** — root
  `INDEX.md`, per-package `SKILL.md`, cross-cutting `aspects/`
  views. The agent navigates with `Read` and `Glob` instead of
  learning a schema.
- **A focused-reads primitive (`read_focused`)** that returns one
  file with non-matching symbols collapsed to one-line markers in
  the file's native comment style. The agent gets the bytes it
  needs without the bytes it doesn't.

The structural MCP tools (`find_symbol`, `get_neighborhood`,
`impact_analysis`, …) are still there for programmatic use, but
they're no longer the headline. See [docs/adoption.md](./docs/adoption.md)
for how to verify your agent is actually using mycelium and how to
read the telemetry log when it isn't.

## Why deterministic AST graphs?

Mycelium parses code with Go/AST and tree-sitter and stores typed,
deterministic edges (`call`, `import`, `type_ref`, `inherit`). It
deliberately does **not** ask an LLM to extract structure during
indexing. That choice is deliberate, and recent independent research
quantifies why.

Chinthareddy (2026) benchmarked three retrieval pipelines on three Java
codebases (Shopizer, ThingsBoard, OpenMRS Core), each evaluated against
a fixed 15-question architecture-tracing suite. The results match
mycelium's own design:

| Pipeline | Correctness (Shopizer) | Indexing time | End-to-end cost |
|---|---|---|---|
| Vector-only RAG | 6/15 | seconds | 1.0× (baseline) |
| LLM-extracted KG | 13/15 | minutes | ~20-45× |
| **AST-derived graph** | **15/15** | **seconds** | **~2×** |

Critically, LLM-mediated extraction silently skipped 31% of input files
during indexing on Shopizer — schema-bound JSON output that the model
just didn't return. Files that vanish at index time become retrieval
blind spots at query time. AST parsing has no such failure mode: every
file the parser accepts is indexed completely.

Mycelium is in the third row: deterministic AST graph, runtime cost
near the vector baseline, no probabilistic file-skipping. Full
attribution and other research that has shaped the design lives in
[docs/research.md](./docs/research.md).

## Status

**v3.0-rc** — agent-native release. Adds the
`.mycelium/skills/` filesystem (Pillar H, v2.3 + v2.5 incremental
regen), `read_focused` and the `focus` parameter on existing
read tools (Pillar I, v2.4), opt-in adoption telemetry (Pillar K,
v2.2), and interface-consumer expansion in `get_neighborhood` /
`impact_analysis` / `critical_path` (Pillar J, v2.1). Builds on
v1.x's type-aware resolvers, workspace mode, PR-scoped queries,
and optional sqlite-vec fast path. Cross-repo federation stays a
v4 non-goal (see [LIMITATIONS.md](./LIMITATIONS.md)).

## Two ways agents use mycelium

### 1. The skills tree (`.mycelium/skills/`)

Run once after indexing:

```bash
myco skills compile         # whole tree, ~100 ms on the self-index
myco skills compile --incremental  # hash-gated; rewrites only changed files
```

The daemon then keeps the tree fresh on every file change (debounced
batches, hash-gated writes — only the SKILL.md files whose rendered
content actually changed get rewritten).

Layout:

```
.mycelium/skills/
├── INDEX.md                 (entry point: every package, language, file/symbol counts)
├── packages/
│   ├── internal/query/SKILL.md   (top-level symbols, top inbound/outbound callers)
│   ├── internal/index/SKILL.md
│   └── ...
└── aspects/
    ├── error-handling/INDEX.md   (Go signatures returning error)
    ├── context-propagation/INDEX.md
    ├── config-loading/INDEX.md   (heuristic, frontmatter-flagged)
    └── logging/INDEX.md          (heuristic, frontmatter-flagged)
```

A worked navigation trace lives in
[docs/navigation-example.md](./docs/navigation-example.md): an agent
answering "which package is the only reader of the SQLite index?"
using only `Read` on `.mycelium/skills/`.

### 2. Focused reads

When the agent does need a specific file, `read_focused` (or
`myco read --focus`) returns it with non-matching symbols collapsed
to one-line markers and matching symbols expanded in full:

```bash
$ myco read cmd/myco/main.go --focus "telemetry recorder" --stats
# cmd/myco/main.go  focus="telemetry recorder"  expanded=3/47  bytes=8443/44337
# (file body with 44 collapsed-to-one-line markers and 3 full functions)
```

Self-index measurements: 81% byte reduction on `cmd/myco/main.go`
with `focus="telemetry recorder"`; 29% on `internal/daemon/daemon.go`
with `focus="dispatch read_focused"`. Smaller files with denser
inter-symbol coupling collapse less aggressively — that's the
honest cost of a deterministic, no-neural-model filter.

The same `focus` parameter is also available on `find_symbol`,
`get_file_outline`, and `get_neighborhood` — all backward-
compatible (empty focus = pre-v2.4 behaviour).

### 3. Structural MCP tools (still available, demoted)

For programmatic use, the structural tools are unchanged. See
[MCP tools exposed](#mcp-tools-exposed) below.

## Install

### From release binaries (recommended)

Grab the tarball for your platform from
[GitHub Releases](https://github.com/jdwiederstein/mycelium/releases).
Each archive unpacks to a self-contained directory containing the
`myco` binary and a bundled `sqlite-vec` shared library at
`lib/vec0.*`. The binary auto-discovers the bundled library, so the
vec0 fast path for `search_semantic` works with zero extra config.

```bash
# Linux amd64
curl -sSL https://github.com/jdwiederstein/mycelium/releases/latest/download/myco-linux-amd64.tar.gz \
  | tar -xz -C /opt && sudo ln -sf /opt/myco-linux-amd64/myco /usr/local/bin/myco

# macOS arm64 (Apple Silicon)
curl -sSL https://github.com/jdwiederstein/mycelium/releases/latest/download/myco-darwin-arm64.tar.gz \
  | tar -xz -C /opt && sudo ln -sf /opt/myco-darwin-arm64/myco /usr/local/bin/myco
```

`DefaultExtensionPath` follows the symlink and looks for `lib/vec0.*`
next to the resolved binary, so the install layout above is the
recommended one. If you'd rather not symlink, run
`/opt/myco-<suffix>/myco` directly — same effect.

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

These are the structural tools for programmatic use. The
[skills tree](#1-the-skills-tree-myceliumskills) and
[focused reads](#2-focused-reads) are the v3 headline; this table is
the back end. All tools return JSON; line/col positions are 1-based.

Tools marked with `project?` accept an optional workspace project name
(see [Workspace mode](#workspace-mode)). Unknown project names return
zero hits rather than silently falling back to unscoped, so config typos
surface immediately. Tools marked with `since?` accept a git ref and
restrict results to files changed between `<ref>...HEAD` (see
[PR-scoped queries](#pr-scoped-queries)). Tools marked with `focus?`
accept the v2.4 lexical filter — empty value preserves prior behaviour.

| Tool | Purpose | Key inputs |
|---|---|---|
| `find_symbol` | Fuzzy/exact symbol lookup. | `name`, `kind?`, `limit?`, `project?`, `since?`, `focus?` |
| `get_definition` | Source span + snippet. | `symbol_id` or `qualified_name` |
| `get_references` | Callers / importers / type uses; flags each hit as resolved vs textual. Fans out through interface implementations (v2.1). | `target`, `limit?`, `project?`, `since?` |
| `read_focused` | Returns one file with non-matching symbols collapsed to one-line markers in the file's native comment style (v2.4). Empty `focus` returns the file in full. | `path`, `focus?` |
| `search_semantic` | Vector search over code chunks (requires embedder). | `query`, `k?`, `kind?`, `path_contains?`, `project?`, `since?` |
| `search_lexical` | Ripgrep-style regex over indexed files. | `pattern`, `path_contains?`, `k?`, `project?`, `since?` |
| `list_files` | Indexed files with language tags. | `language?`, `name_contains?`, `limit?`, `project?`, `since?` |
| `get_file_outline` | Hierarchical symbol tree for one file. | `path`, `focus?` |
| `get_file_summary` | Structural summary (exports, imports, LOC). | `path` |
| `get_neighborhood` | Local call graph around a symbol (recursive CTE on refs). Seed lookup respects `project?`; traversal stays global so cross-project edges still surface. Walks `RefInherit` edges so interface consumers fan in (v2.1). | `target`, `depth?`, `direction?`, `project?`, `focus?` |
| `impact_analysis` | Transitive inbound closure ranked by distance (v1.6). For "who's impacted if I change this?" or (with `kind=method`) "what tests cover this?". Interface-aware (v2.1). | `target`, `kind?`, `depth?`, `project?`, `since?` |
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
query — fine below ~10k chunks, painful above.

**Release tarballs (v3.0-rc+) ship a matching
[sqlite-vec](https://github.com/asg017/sqlite-vec) build at
`lib/vec0.*` next to the `myco` binary.** `internal/index.DefaultExtensionPath`
auto-discovers it, so on a release install the vec0 fast path is on by
default — no config edit needed. The vec0 version we pin is in
`.github/workflows/release.yml`.

Resolution order (first hit wins):

1. `index.vector.extension_path` in `.mycelium.yml` — explicit user
   override, always preferred.
2. `<exe-dir>/lib/vec0.<ext>` — release-tarball layout.
3. `<exe-dir>/vec0.<ext>` — same dir as the binary, in case someone
   unpacked the archive flat.
4. Empty — brute-force cosine fallback.

Source builds (`go install` / `go build`) don't ship with a bundled
library, so on those installs you'll want to either install
sqlite-vec system-wide or point `extension_path` at a downloaded
build. Example on Linux amd64:

```bash
# Pick the right prebuilt .so/.dylib/.dll for your platform from
#   https://github.com/asg017/sqlite-vec/releases
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

### Benchmark

Measured on an Intel i7-1165G7 laptop (Tiger Lake, 4c/8t, 2.8 GHz),
random unit-norm vectors, `k=10`, 3 iterations per cell, mean ns/op:

| corpus | dim | brute-force | vec0    | speedup |
|--------|-----|-------------|---------|---------|
| 10k    | 384 |    72 ms    |  11 ms  |  6.6×   |
| 10k    | 768 |   106 ms    |  19 ms  |  5.7×   |
| 10k    | 1536|   193 ms    |  37 ms  |  5.2×   |
| 50k    | 384 |   365 ms    |  45 ms  |  8.1×   |
| 50k    | 768 |   552 ms    |  95 ms  |  5.8×   |
| 50k    | 1536|  1078 ms    | 172 ms  |  6.3×   |
| 100k   | 384 |   697 ms    |  89 ms  |  7.8×   |
| 100k   | 768 |  1094 ms    | 171 ms  |  6.4×   |
| 100k   | 1536|  2130 ms    | 345 ms  |  6.2×   |

**Reading the table.** Both paths scale linearly in the number of
chunks. vec0 at v0.1.9 is SIMD-optimized flat scan in C — faster than
the pure-Go cosine loop by a constant 5-8×, but not sub-linear. HNSW
is on sqlite-vec's roadmap; when it lands, this table will look very
different.

**What this means for your repo:**

- Below ~10k chunks: either path is fast enough.
- 10k–50k chunks: install sqlite-vec and you get interactive latency
  (<100 ms) across all practical embedding dimensions.
- 50k–100k chunks: vec0 keeps you under ~350 ms even at 1536 dims;
  brute-force is no longer interactive.
- Above 100k: extrapolate linearly; treat vec0 as necessary, not
  optional.

The full benchmark lives in `internal/query/semantic_bench_test.go`.
Reproduce with:

```bash
# Brute-force only (no extension needed):
go test -tags sqlite_fts5 -run=^$ \
  -bench=BenchmarkSemanticSearch -benchtime=3x -count=1 \
  ./internal/query/

# Both paths (set MYCELIUM_VEC_PATH to your vec0.so / vec0.dylib / vec0.dll):
MYCELIUM_VEC_PATH=/usr/local/lib/mycelium/vec0.so \
go test -tags sqlite_fts5 -run=^$ \
  -bench=BenchmarkSemanticSearch -benchtime=3x -count=1 \
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

## Adoption: is your agent actually using mycelium?

Mycelium is only useful if the agent reaches for it. Turn on telemetry
to see which tools your agent actually calls:

```yaml
# .mycelium.yml
telemetry:
  enabled: true
```

Restart the daemon, run a normal session for a day, then aggregate:

```bash
myco stats --telemetry
# tool                   count    ok    in_bytes   out_bytes   p50    p95
# find_symbol               87    87       3.6KB      94.2KB    6ms   18ms
# get_neighborhood          54    54       2.1KB     217.8KB   12ms   41ms
# read_focused              19    19       0.8KB      72.6KB    7ms   22ms
# ...
```

The log is local-only (`.mycelium/telemetry.jsonl`), one JSON object per
call. [docs/adoption.md](./docs/adoption.md) covers the five-minute
setup checklist, common shapes of "agent isn't using mycelium" with
fixes, and reference numbers for what a healthy session looks like.

## Contributing

See [CONTEXT.md](./CONTEXT.md) for the architectural rules — in particular,
the "one writer, one reader" boundary, the no-cgo-in-Go-parser rule, and
why semantic search defaults to off.

## License

TBD. The repository is currently unlicensed; a license will be chosen before
the first tagged release.
