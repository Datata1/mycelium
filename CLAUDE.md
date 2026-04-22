# Mycelium — working notes for Claude Code

Mycelium is a local repository knowledge base for AI coding agents. The binary
is `myco`; storage is a single SQLite file at `.mycelium/index.db`; three
transports (MCP stdio, unix socket, HTTP :7777 loopback) share one dispatcher.

See `README.md` for user-facing docs, `CONTEXT.md` for the problem + goals,
`LIMITATIONS.md` for what doesn't work today (read before proposing new
features — the list covers most "could we…?" questions), and `CHANGELOG.md`
for version history. The active roadmap is the v2.0 plan at
`~/.claude/plans/1-everything-you-mentioned-indexed-duckling.md`.

## Build + run (required every session)

Always build with the `sqlite_fts5` tag — it enables FTS5 in the embedded SQLite
driver, without which migrations fail.

```bash
go build -tags sqlite_fts5 -o /tmp/myco ./cmd/myco
go test  -tags sqlite_fts5 -race ./...
go vet   -tags sqlite_fts5 ./...
```

Fastest smoke-test cycle: `rm -rf .mycelium && /tmp/myco index && /tmp/myco doctor`.

## Load-bearing architectural rules

Enforce in review. Deviations need an explicit reason in CHANGELOG.

- **Daemon is the only SQLite writer.** MCP, HTTP, CLI, and git hooks are thin
  clients over `.mycelium/daemon.sock`.
- **`internal/query` is the only reader.** `internal/pipeline` is the only
  writer. Transports never issue raw SQL.
- **Parsers emit plain structs.** `internal/parser/*` knows nothing about
  storage. Storage code (`internal/index`) knows nothing about languages.
- **Hash-driven re-embeds.** `files.content_hash` + `symbols.symbol_hash` mean
  formatting-only changes cost zero embedding calls. `embed_cache` is keyed by
  content_hash so renames are free.
- **Embedder defaults to `none`.** Don't assume Ollama is installed.
- **No LLM calls during indexing.** Indexing must be deterministic, offline,
  and free. Enrichment is opt-in and lives in its own pass.
- **No pre-commit hook ever.** Post-commit only.
- **Go parser uses stdlib `go/ast`** (no cgo). Only TS and Python use tree-sitter.
- **Distribution = GitHub Releases binaries.** No Homebrew, no `go install`,
  no Docker (fsnotify through bind-mounts is unreliable).

## Roadmap status (as of last session)

- **v1.0 shipped** — 9 MCP tools, 3 transports, 3 languages (Go/TS/Python).
- **v1.1 shipped** — `myco doctor`, extended `query.Stats`, benchmark harness.
- **v1.2 shipped** — Go type resolver (`internal/resolver/golang/`). Self-index
  now shows 0 resolution-bug self-loops and 0% truly-unresolved refs.
- **Active next: v1.3 "TS and Python scope resolvers"** — scope-tracking
  tree walkers for TypeScript (handles import bindings, class fields,
  `this`-typed method calls) and Python (scope + import resolution).
  Bumps `resolver_version` to 2 (TS) and 3 (Python). Target:
  `unresolved_ref_ratio < 20%` on a mid-size TS repo, `< 15%` on Python.
  Explicit non-goals for TS: generics resolution, conditional types,
  declaration merging, ambient modules beyond `tsconfig.paths`.

**v1.2 baselines achieved (self-index, Tiger Lake laptop):**
- `self_loop_count`: 11 → **0** ✓
- `unresolved_ref_ratio` (non-import, v0+null): 74.8% → **0.0%** ✓
- 550 refs resolved to local symbols; 1425 type-resolved external
- 10k-symbol benchmark: 2347 sym/sec (−3.5% vs v1.1)

When diagnosing v1.2 resolution issues, set `MYCELIUM_RESOLVER_DEBUG=1`
for per-file visit/rewrite counts on stderr.

## Dogfooding — use mycelium to develop mycelium

Once the daemon + MCP are wired into Claude Code, reach for mycelium's own
tools before `grep`/`Read` whenever it's applicable.

One-time setup:

```bash
cd /home/jan-david/Documents/repo-graph
/tmp/myco daemon &                 # background watcher + index server
/tmp/myco init --mcp claude        # prints the JSON for ~/.claude.json
# paste the printed snippet into mcpServers in ~/.claude.json
# then restart Claude Code — MCP servers load at startup
```

Once wired, these tools are available (subset):
`find_symbol`, `get_references`, `get_definition`, `get_neighborhood`,
`search_lexical`, `search_semantic`, `get_file_outline`, `get_file_summary`,
`stats`.

**Honest caveat**: until v1.2 ships, `get_references` and `get_neighborhood`
are quality-limited on this repo by the 72.8% unresolved-ref problem (v1.2's
whole point). Results are still useful but not authoritative.

## Conventions

- **Comments**: only when the *why* is non-obvious. No explaining what code
  does; names carry that. No references to "the current task", "for the X
  flow", etc. — those belong in PR descriptions.
- **Tests**: every new query method gets a case in `integration_test.go`.
  Benchmarks for anything on the query hot path.
- **Migrations** (`internal/index/migrations/*.sql`) are additive — never
  rewrite a shipped file. New numbered file each change.
- **Commit messages**: imperative present ("add X"), not "added X".
- **CHANGELOG**: one section per milestone, Keep-a-Changelog format. Every
  milestone ends with a CHANGELOG entry.

## Memory

Persistent project memory lives at
`~/.claude/projects/-home-jan-david-Documents-repo-graph/memory/`. Update it
when you learn something durable (architectural decisions, user preferences,
baseline numbers). Don't duplicate CHANGELOG/README content there — memory
is for facts that aren't obvious from reading the code or git log.
