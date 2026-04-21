# Mycelium — Context & Goals

> A repository knowledge base for AI coding agents.
> CLI: `myco` · Storage: local SQLite · Interfaces: MCP (stdio), HTTP, CLI

---

## The problem

Every AI coding agent working on a repo starts cold. It greps for a name it half-remembers, reads three irrelevant files, opens a fourth by accident, and only then starts on the actual task. Across a long session this burns thousands of tokens and minutes of wall-clock time, and the user pays for both.

Existing tools don't close the gap:

- **Ripgrep / grep** — fast for literals, blind to structure. "Find all callers of `ParseRequest`" returns every string containing those letters.
- **Language servers** — precise, but per-language, heavy, and not designed to be queried by non-human clients.
- **Full-text indexes (Sourcegraph, Zoekt)** — great for humans browsing code; not available locally on every dev's machine and not shaped for agent queries.
- **Vector search over whole files** — coarse, expensive, and stale the moment someone saves a file.

Agents need something in between: fast enough to query on every tool call, structured enough to return *the right function* rather than *the right page*, and cheap enough to keep continuously fresh as the developer edits code.

## What mycelium is

Mycelium is a single-binary Go tool (`myco`) that any repository can opt into with one command. It runs a small daemon in the background that:

1. **Parses** every source file with tree-sitter into a structural model — symbols, their signatures, their references, their docstrings.
2. **Stores** that model plus optional code embeddings in a local SQLite database at `.mycelium/index.db`.
3. **Updates** incrementally as files change — watched by fsnotify, backstopped by a post-commit git hook.
4. **Serves** the index to AI agents over the Model Context Protocol (MCP), plus an HTTP API and CLI for scripts.

The name comes from mycelial networks — the underground fungal threads that connect every tree in a forest, letting them share resources and signals. That is the role of this tool for a codebase: a quiet substrate underneath the source tree that lets agents find each other's work.

## Who it's for

- **Developers using AI coding agents** (Claude Code, Cursor, Aider, custom MCP clients) on projects large enough that the agent regularly loses its way.
- **Agents themselves** — they are the primary *query* clients.
- **Team leads** who want every agent on the team to share the same live, structured view of the repo without running a central service.

Not for: users who only want fuzzy full-text search (use ripgrep), or teams who need a hosted multi-repo solution (use Sourcegraph).

## Goals

- **Single binary, per-repo, self-contained.** Clone the repo, run `myco init`, and everything lives under `.mycelium/`. No external services, no Docker, no database to host.
- **Always fresh.** The watcher keeps the index within a few hundred milliseconds of the files on disk. Agents never see stale data from a previous edit.
- **Cheap to update.** Formatting-only changes re-embed nothing. Typical edit sessions touch a handful of chunks. Embedding spend stays proportional to *real* code change, not save events.
- **Precise queries.** Agents can ask for a symbol, its definition, its references, its neighborhood, or a semantic match — and get typed results with `file:line` anchors, not prose.
- **Multi-language from day one.** First-class support for Go, TypeScript, and Python. Adding a language is a tree-sitter query file plus a symbol extractor, not a rewrite.
- **Honest about what it doesn't know.** References flag themselves as resolved vs. textual-only; the index exposes `stats()` so agents can tell whether to trust it.

## Non-goals (v1.0)

Holding the scope tight so v1.0 ships:

- **Type-perfect cross-file resolution.** We build a best-effort symbol graph from tree-sitter, not an LSP-grade type resolver. Textual `dst_name` refs are kept alongside resolved ones so callers can see what's verified.
- **LLM-generated summaries at index time.** Too slow, too costly, nondeterministic. v1.0 returns structural summaries only (exports, imports, LOC, top comment). LLM enrichment is a v1.1+ opt-in.
- **Multi-repo / workspace mode.** One repo per index. Monorepos with many logical projects still work — the repo *is* the unit.
- **Docker image.** fsnotify through a container bind mount is unreliable. We ship native binaries only.
- **Hosted service.** Mycelium is a local tool. There is no cloud component.
- **Pre-commit hooks.** Blocking commits for indexing is user-hostile. The post-commit hook is the only git integration.

## Architectural rules

These are load-bearing and should be enforced in review:

1. **The daemon is the only writer.** Every mutation goes through `internal/pipeline`. MCP server, HTTP server, CLI, and the git hook are thin clients that talk to the daemon over a unix socket. This eliminates multi-writer SQLite races.
2. **`internal/query` is the only reader.** MCP, HTTP, and CLI all call the same query package; nobody writes raw SQL in a transport layer. This keeps the agent-facing surface consistent across interfaces.
3. **Parsers know nothing about storage.** `internal/parser/*` emits plain `Symbol` and `Reference` structs. Storage code knows nothing about languages.
4. **Hash, don't re-embed.** Files are keyed by `content_hash`, symbols by `symbol_hash` (signature+body). Reformatting shouldn't cost an embedding call. An `embed_cache` keyed by content hash ensures renames and moves are free.
5. **No LLM calls during indexing.** Indexing must be deterministic, offline-capable, and free. LLM-based enrichment, if added, runs as a separate opt-in pass with its own cache.
6. **Embedder is pluggable and defaults to `none`.** Semantic search is a value-add, not a prerequisite. `find_symbol`, `search_lexical`, `get_references`, and the rest work without any embedder configured.
7. **No raw SQL endpoint for agents.** The MCP surface is a fixed set of typed tools. Agents don't get a shell into the database.

## Interfaces

Mycelium exposes the index through three equivalent surfaces, all backed by `internal/query`:

- **MCP stdio server** (`myco mcp`) — primary interface for AI agents. Tools include `find_symbol`, `get_definition`, `get_references`, `search_semantic`, `search_lexical`, `list_files`, `get_file_outline`, `get_file_summary`, `get_neighborhood`, and `stats`.
- **HTTP API** (`myco daemon` on `:7777` loopback) — same surface as MCP, for scripts and non-MCP agents.
- **CLI** (`myco query ...`) — for humans debugging the index or piping into shell tools.

## Roadmap

See the implementation plan at `/home/jan-david/.claude/plans/1-everything-you-mentioned-indexed-duckling.md` for phased milestones (v0.1 → v1.0). Short version:

- **v0.1** — one-shot Go-only indexer + lexical `find_symbol`.
- **v0.2** — fsnotify watcher + daemon + references.
- **v0.3** — MCP server + TS/Python parsers + post-commit hook.
- **v0.4** — embeddings + `search_semantic`.
- **v0.5** — performance pass, HTTP API, remaining MCP tools.
- **v1.0** — cross-platform release binaries, docs, integration tests.

## Success criteria

Mycelium is working if:

- An agent's average "time to first relevant file" drops noticeably on a repo where it's installed.
- Index lag stays under a second for interactive edits.
- Typical save events cost zero embedding API calls.
- Adding a new language takes a weekend, not a rewrite.
- A developer can install it, run `myco init`, and forget it exists.
