# Mycelium — working notes for Claude Code

Mycelium is a local repository knowledge base for AI coding agents. The binary
is `myco`; storage is a single SQLite file at `.mycelium/index.db`; three
transports (MCP stdio, unix socket, HTTP :7777 loopback) share one dispatcher.

See `README.md` for user-facing docs, `docs/limitations.md` for what doesn't
work today (read before proposing new features), `CHANGELOG.md` for version
history, and `docs/adoption.md` for guidance on verifying that an agent is
actually reaching for myco tools.

## Problem and goals

Mycelium gives AI coding agents structural, always-fresh knowledge of a repo
without external services. Agents can ask for a symbol, its callers, its
neighborhood, or a lexical match and get typed results with `file:line` anchors.

Non-goals (current): type-perfect cross-file resolution, LLM summaries at index
time, cross-repo federation, Docker image, hosted service, pre-commit hooks.

## Build + run

Always build with the `sqlite_fts5` tag — it enables FTS5 in the embedded
SQLite driver, without which migrations fail.

```bash
task build          # → ~/.local/bin/myco  (dev artifact, no sudo)
task install        # → overwrites the system-wide `myco` on PATH
task check          # go vet + go test -race ./...
task smoke          # wipe index → re-index → myco doctor (fastest loop)
task daemon         # build + start daemon (blocks)
```

`task install` is the only correct way to "install the dev build" over a
prior install. It resolves `command -v myco`, follows one level of symlink,
and overwrites the target — so a build always replaces the binary that
`myco` actually runs from. A bare `task build` only writes to
`~/.local/bin/myco`, which is shadowed on macOS where `/usr/local/bin`
precedes `~/.local/bin` in PATH (the layout the README's release-binary
install produces).

`task` is installed via `go install github.com/go-task/task/v3/cmd/task@latest`
and lives at `$(go env GOPATH)/bin/task`. Run `task --list` for all targets.

Raw commands (when task isn't on PATH):

```bash
go build -tags sqlite_fts5 -o /tmp/myco ./cmd/myco
go test  -tags sqlite_fts5 -race ./...
go vet   -tags sqlite_fts5 ./...
```

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
- **Migrations** (`internal/index/migrations/*.sql`) are additive — never
  rewrite a shipped file. New numbered file per schema change.

## Roadmap status

- **v1.0** — 9 MCP tools, 3 transports, 3 languages (Go/TS/Python).
- **v1.1** — `myco doctor`, extended `query.Stats`, benchmark harness.
- **v1.2** — Go type resolver (`internal/resolver/golang/`). Self-index:
  0 self-loops, 0.0% unresolved refs.
- **v1.3** — TS resolver (ResolverVersion=2) + Python resolver
  (ResolverVersion=3). Scope-tracking walkers; handle imports,
  `this.method()`, `self.method()`, namespace + aliased imports.
- **v1.4** — sqlite-vec integration (`internal/index/vss.go`). Brute-force
  Go cosine fallback. Two-pass search: 10k/768dim brute-force 166→114ms.
- **v1.5** — Workspace mode. `projects` table, nullable `files.project_id`,
  per-project config overrides, `project` filter on every query tool.
- **v1.6** — Graph-native tools (`impact_analysis`, `critical_path`) +
  `--since <ref>` PR-scope filter on five read methods.
- **v1.7** — Watchman opt-in backend. Pluggable `internal/watch.Watcher`
  interface; `watcher.backend: watchman` in config; inotify headroom check
  in `myco doctor`.
- **v2.0-rc1** — Consolidation tag for the v1.x series; all v2.0 acceptance
  criteria met (type-aware refs, workspace, graph tools, doctor, vec).
- **v2.2** — Opt-in telemetry log (`internal/telemetry`). JSONL per-call
  recorder; `myco stats --telemetry` aggregator. Off by default.
- **v2.4** — Focused reads. `internal/focus` lexical filter; `read_focused`
  MCP tool + `myco read`; `--focus` on `find_symbol`, `get_file_outline`,
  `get_neighborhood`. Typical 80% byte reduction on large files.
- **v3.1** — Adoption fixes from first TS-monorepo field test:
  `FindSymbolResult{Matches,Hints}` envelope (no more `null` on miss),
  MCP tool descriptions rewritten for first-reach priming, `Stats.ConfiguredProjects`
  + `projects_configured_but_empty` doctor check.
- **Session telemetry** — `myco session` command group: per-conversation sessions,
  automatic Claude Code hook wiring, fallback-tool tracking. `telemetry.enabled: true`
  is set in `.mycelium.yml`.
- **v4.0** — Adoption fixed-point, bug triage, polish.
- **v5.0 (current)** — Cleanup milestone: embeddings removed, skills compilation
  removed, cmd package split, integration tests moved to `test/integration/`,
  language extension contract documented in `docs/adding-a-language.md`.

## Dogfooding — use mycelium to develop mycelium

Reach for myco tools before `grep`/`Read` whenever applicable. Check
`docs/adoption.md` if the agent is falling into the "search_lexical only"
pattern — that doc describes the common failure modes and how to diagnose them.

One-time setup:

```bash
task daemon &                      # build + start daemon in background
/tmp/myco init --mcp claude        # prints the JSON snippet for ~/.claude.json
# paste into mcpServers in ~/.claude.json, then restart Claude Code
```

For session telemetry (tracking which myco tools vs. grep/Read the agent
uses per conversation):

```bash
task hooks-install    # writes UserPromptSubmit/PostToolUse/Stop hooks
                      # to .claude/settings.json — restart Claude Code after
task session-list     # see recorded sessions
task session-export -- <id>           # full report: myco calls + fallback tools
task session-export -- <id> --format markdown
task session-compare -- <id-a> <id-b> # side-by-side diff
```

Sessions start automatically when Claude Code hook fires `UserPromptSubmit`.
The key metric is `fallback_exploratory` — how many grep/Read calls the agent
made instead of using myco. Low ratio = myco is covering the use case.

Available MCP tools (all active):
`find_symbol`, `get_references`, `get_neighborhood`,
`search_lexical`, `get_file_outline`, `get_file_summary`, `read_focused`,
`impact_analysis`, `critical_path`, `stats`, `list_files`, `find_document_key`.

## Conventions

- **Comments**: only when the *why* is non-obvious. No explaining what code
  does; names carry that. No references to "the current task" or caller names.
- **Tests**: every new query method gets a case in `integration_test.go`.
  Benchmarks for anything on the query hot path.
- **Migrations**: additive only — new numbered file, never rewrite shipped ones.
- **Commit messages**: imperative present ("add X"), not "added X".
- **CHANGELOG**: one section per milestone, Keep-a-Changelog format. Every
  milestone ends with a CHANGELOG entry.

## Memory

Persistent project memory lives at
`~/.claude/projects/-home-jan-david-Documents-repo-graph/memory/`. Update it
when you learn something durable (architectural decisions, user preferences,
baseline numbers). Don't duplicate CHANGELOG/README content there — memory
is for facts that aren't obvious from reading the code or git log.

## mycelium (myco)

myco is a local code knowledge base exposed as MCP tools. Reach for it
**before** `Bash(grep)` or `Read` for any code navigation task.

**Navigation:** `find_symbol` (definitions) · `get_references` (callers) ·
`read_focused` (read a file with irrelevant symbols collapsed) ·
`get_neighborhood` (local call graph) · `impact_analysis` (what depends on X)

**Rule:** when you have an identifier name, use `find_symbol` — not
`search_lexical`. `search_lexical` is for literal strings and regex patterns
only (log messages, route paths, magic constants). Using it for symbol names
misses renames, aliases, and qualified forms.
