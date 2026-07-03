# Contributing to Mycelium

Thanks for your interest! This document covers everything you need to build,
test, and land a change.

## Prerequisites

- **Go** (version pinned in [go.mod](./go.mod) — CI uses exactly that version
  via `go-version-file`)
- **A C toolchain** — the TypeScript/Python parsers use tree-sitter and the
  SQLite driver is cgo, so `CGO_ENABLED=1` is mandatory
- **[Task](https://taskfile.dev)** (optional but recommended):
  `go install github.com/go-task/task/v3/cmd/task@latest`

## Building and testing

Every build, test, and lint command **must** carry the `sqlite_fts5` build
tag. It enables FTS5 in the embedded SQLite driver; without it, migrations
fail at startup.

```bash
task build      # → ~/.local/bin/myco
task check      # go vet + go test -race ./...   — run before every push
task smoke      # wipe index → re-index → myco doctor (fastest feedback loop)
task daemon     # build + start the daemon (blocks)
```

Without Task:

```bash
go build -tags sqlite_fts5 -o /tmp/myco ./cmd/myco
go test  -tags sqlite_fts5 -race ./...
go vet   -tags sqlite_fts5 ./...
```

## Architecture in one page

Mycelium indexes a repo into a single SQLite file (`.mycelium/index.db`) and
serves typed queries to AI agents over three transports (MCP stdio, unix
socket, HTTP loopback) that share one dispatcher.

```
parsers (internal/parser/*)  →  pipeline (internal/pipeline)  →  SQLite (internal/index)
                                                                      ↓
transports (mcp / http / cli) →  daemon dispatcher (internal/daemon) →  reads (internal/query)
```

Start with [README.md](./README.md), then [docs/limitations.md](./docs/limitations.md)
(read this before proposing features), and
[docs/adding-a-language.md](./docs/adding-a-language.md) for language support.

### Adding a query tool

One entry per layer — the parity test in `internal/registry` fails with the
name of anything you forget:

1. Schema + agent-facing description: `pkg/mcpschema/tools.go`
2. Wire types + method const (+ `AllMethods`): `internal/ipc`
3. Typed execution method: `internal/service`
4. One table row: `internal/registry/registry.go`
5. Renderer + golden test case: `internal/mcp/render` +
   `internal/registry/golden_test.go` (regenerate with `-update`)
6. CLI subcommand (flags → params → `callRead`): `cmd/myco/cmd_query.go`

### Invariants — enforced in review, non-negotiable

1. **The daemon is the only SQLite writer.** MCP, HTTP, CLI, and git hooks are
   thin clients over `.mycelium/daemon.sock`.
2. **`internal/query` is the only reader; `internal/pipeline` the only
   writer.** Transports never issue raw SQL.
3. **Parsers emit plain structs** and know nothing about storage; storage
   knows nothing about languages. The Go parser stays stdlib `go/ast`
   (no cgo for Go).
4. **Indexing is deterministic, offline, and free** — no LLM or network calls
   during indexing.
5. **Migrations are additive only** — a new numbered file in
   `internal/index/migrations/`, never a rewrite of a shipped one.
6. **No pre-commit hooks, ever.** Post-commit only.
7. **Distribution is GitHub Releases binaries only** — no Homebrew,
   `go install`, or Docker.

Deviations need an explicit rationale in the CHANGELOG.

## Making changes

- **Branch + PR** against `main`. CI runs vet, build, tests (`-race`), and
  lint on Linux and macOS.
- **Lint**: `golangci-lint run --build-tags sqlite_fts5 ./...` must be clean.
- **Tests**: every new `internal/query` method gets a case in
  `test/integration/`; anything on the query hot path gets a benchmark.
  Golden-file diffs (`testdata/golden/`) are code — review them, and only
  regenerate deliberately (`go test <pkg> -update`).
- **Commit messages**: imperative present ("add X", not "added X").
- **CHANGELOG.md**: user-visible changes get an entry (Keep-a-Changelog
  format).
- **Comments**: only where the *why* is non-obvious. Names carry the *what*.

## Reporting bugs

Please include your OS, `myco --version`, and the output of `myco doctor` —
the issue template asks for these.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](./LICENSE) (see § 5 of the license).
