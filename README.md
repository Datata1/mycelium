# mycelium

> A local, always-fresh repository knowledge base for AI coding agents.
> Binary: `myco` · Storage: SQLite at `.mycelium/index.db` · Primary interface: MCP

Mycelium parses your repo with tree-sitter, stores symbols, references, and (optionally) semantic embeddings in a single SQLite file, and serves that index to Claude Code, Cursor, and other MCP clients. A background daemon keeps the index within a few hundred milliseconds of what's on disk. No external services, no Docker.

See [CONTEXT.md](./CONTEXT.md) for the problem, goals, and non-goals.

## Status

Pre-v0.1. Scaffolding only. Not yet runnable.

## Install (planned)

Once v1.0 ships, grab the release binary for your platform from GitHub Releases:

```bash
# Linux amd64 example (placeholder; real URL TBD)
curl -L https://github.com/<owner>/mycelium/releases/latest/download/myco-linux-amd64 -o /usr/local/bin/myco
chmod +x /usr/local/bin/myco
```

Supported targets at v1.0: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`.

## Quick start (planned)

```bash
cd my-repo
myco init --mcp claude      # writes .mycelium.yml, installs post-commit hook, registers MCP server in Claude Code
myco daemon &               # start the background watcher + index server
```

That's it. Edit files normally; the daemon keeps the index current. Your agent now has `find_symbol`, `search_semantic`, `get_references`, and friends.

## Config

See [.mycelium.yml.example](./.mycelium.yml.example). Commit `.mycelium.yml`; add `.mycelium/` to `.gitignore` (the indexer does this for you).

## MCP tools exposed

| Tool | What it does |
|---|---|
| `find_symbol` | Fuzzy symbol lookup (FTS5-backed). |
| `get_definition` | Source span + snippet. |
| `get_references` | Callers / importers; flags resolved vs. textual-only. |
| `search_semantic` | Vector search (requires an embedder configured). |
| `search_lexical` | Ripgrep-style substring/regex. |
| `list_files` | Glob listing with language tags. |
| `get_file_outline` | Hierarchical symbol tree for one file. |
| `get_file_summary` | Structural summary (exports, imports, LOC, top comment). |
| `get_neighborhood` | Local call graph around a symbol. |
| `stats` | Index freshness, symbol counts, languages. |

## Development

```bash
# FTS5 is required for fuzzy symbol search; pass the build tag.
go build -tags sqlite_fts5 -o /tmp/myco ./cmd/myco
/tmp/myco index        # one-shot index of the current repo
/tmp/myco query find Pipeline
/tmp/myco stats
go test -tags sqlite_fts5 ./...
```

Requires Go 1.22+ and a C toolchain (`mattn/go-sqlite3` uses cgo; tree-sitter will too once TS/Python parsers land).

## License

TBD.
