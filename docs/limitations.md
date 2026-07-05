# Limitations

An honest catalogue of what mycelium doesn't do today, grouped by whether
it's an intentional cut or a planned improvement. Version pointers refer
to the v2.0 roadmap at `~/.claude/plans/1-everything-you-mentioned-indexed-duckling.md`.

## Resolution quality

| Limitation | Cause | Status |
|---|---|---|
| TS and Python refs use short-name fallback | No scope walker yet | Planned **v1.3** |
| Go resolution degrades to textual when `go/types` can't type-check a package | Broken build graph, missing vendor dir | By design — surface via `myco doctor` LoadErrors |
| No generics-aware resolution for TS (conditional types, declaration merging, ambient modules) | Scope walker cut at "80% of tsc" | **Explicit non-goal** — anything outside the cut stays textual |
| Method calls through interfaces resolve to the interface method, not the concrete impl | `go/types` can't disambiguate without runtime info | By design — matches how the Go compiler itself sees the call |
| Generated code is indexed but may have unresolvable refs (templates, mocks) | `go/packages` loads them but the target symbols may not exist | By design — stays as textual refs |

## Graph queries

Traversal cost at the depth caps is **measured**, not assumed (see the
2026-07-05 CHANGELOG entries; `BenchmarkQueryImpactAnalysis` /
`CriticalPath` / `NeighborhoodDeep` in `internal/pipeline`). On a
50k-symbol fixture with fanout-8 density: `impact_analysis` depth 10 ≈
7.6 ms, `get_neighborhood` depth 5 (both directions) ≈ 14.8 ms,
`critical_path` depth 8 ≈ 1.1 ms. The SQLite-backed design has two
orders of magnitude of headroom before the GraphStore revisit
threshold (~250 ms at the caps).

| Limitation | Cause | Status |
|---|---|---|
| `get_neighborhood` silently caps depth at 5 | Exponential fan-out on dense graphs (result size, not CTE perf — depth 5 dense measures ~15 ms) | Now surfaces a visible note; backend revisit only per the decision rule above |
| `impact_analysis` (transitive test coverage) | — | **Shipped in v1.6** — flat distance-ranked list with optional `kind` filter; composes with `project` / `since` |
| `critical_path` (shortest-path between two symbols) | — | **Shipped in v1.6** — layered BFS in Go over the `refs` table, cycles prevented via visited set, depth ≤ 8. The original all-acyclic-paths CTE degraded to ~24 s on dense graphs (fanout^depth enumeration); rewritten 2026-07-05, now ≈1.1 ms dense at 50k |
| `critical_path` only returns minimal-length paths | Shortest-path DAG enumeration; longer paths are never materialized | By design since the 2026-07-05 rewrite — `k` caps how many shortest paths are listed, not a mix of lengths |
| No cross-repo graph (sibling worktrees sharing one logical graph) | Single SQLite file per worktree; federation not designed | **Explicit v3** — not coming to v2 |
| No `ask(question)` natural-language tool | Violates "no LLM at query time" | **Explicit non-goal** — the calling agent is already an LLM |

## Indexing + scale

> **Stale rows:** v5.0 removed the embeddings subsystem entirely
> (migrations 0008/0009 drop `chunks`, `embed_queue`, `vss_chunks`;
> `internal/index/vss.go` no longer exists). The semantic-search /
> sqlite-vec rows below describe removed features and are kept only
> until the next doc sweep.

| Limitation | Cause | Status |
|---|---|---|
| Semantic search brute-force is slow past ~10k chunks | Pure-Go cosine scan; no SIMD | ~~Optional sqlite-vec integration shipped in v1.4~~ — **removed in v5.0** along with all embedding support |
| Project-scoped semantic search skips the vec0 fast path | `vec0 MATCH` doesn't compose with arbitrary `WHERE` clauses | **Removed in v5.0** |
| `--since`-scoped semantic search skips the vec0 fast path | Same root cause: `vec0 MATCH` + path-IN filter don't compose | **Removed in v5.0** |
| Files with no extracted symbols (SQL, Markdown, config) get no embedding | `chunker.FromSymbols` is symbol-level only | **Removed in v5.0** — document parsers (v3.3) cover the structured-file cases instead |
| Can't index multiple sub-projects with per-project config overrides | Flat `files` table, no `project_id` | **Shipped in v1.5** — `projects:` list in `.mycelium.yml` plus an optional `project` filter on every query tool. One daemon, one SQLite, N sub-projects inside one worktree. Cross-repo federation (N worktrees → one graph) stays **v3** |
| PR-scoped `--since <ref>` filter on queries | — | **Shipped in v1.6** — resolved via `git diff --name-only <ref>...HEAD` at the transport boundary. Hard-capped at 500 changed files (SQLite 999-param limit); beyond that, the user is told to pick a tighter base ref rather than silently truncate |
| fsnotify hits inotify limits on 100k+ file repos (default `fs.inotify.max_user_watches = 8192`) | Linux kernel cap | Planned **v1.7** Watchman opt-in backend |
| Editor atomic-save (vim, some VSCode setups) delivers CREATE+DELETE instead of MODIFY | fsnotify platform behavior | Partially mitigated by `embed_cache` + post-commit catch-up; untouched otherwise |

## Distribution + runtime

| Limitation | Cause | Status |
|---|---|---|
| Binary requires cgo (tree-sitter, mattn/go-sqlite3) — no pure-static Linux build | Dependency reality | **Explicit non-goal** to remove |
| Requires `-tags sqlite_fts5` at build time | mattn driver gates FTS5 behind a tag | By design, documented in README |
| Requires Go 1.25+ to build from source | `golang.org/x/tools` dependency | Accepted as of v1.2 |
| No Docker image | fsnotify through a bind-mount is unreliable | **Explicit non-goal** |
| Daemon isn't auto-started on system boot | v1.0 cut | Post-v1.0 — systemd/launchd user unit, not urgent |
| HTTP API is loopback only, no auth | "Local tool" ethos | **Explicit non-goal** — if we ever host, auth is a rewrite |
| One daemon per repo; MCP restart required to pick up config changes | Claude Code loads MCP servers at startup | Claude Code constraint, not ours |

## Tooling + MCP surface

| Limitation | Cause | Status |
|---|---|---|
| `search_semantic` requires an embedder (Ollama or API) | Embeddings cost, provider-neutral surface | **Removed in v5.0** — the tool no longer exists |
| Indexing is deterministic and free — no LLM-generated summaries | Explicit architectural rule | By design — revisit as opt-in in v1.1+ if demand |
| No pre-commit git hook | Blocks commits for indexing = user-hostile | **Explicit non-goal** |
| No auto-refresh of MCP tool list when new versions ship | MCP spec requires client restart | Client-side, not our problem |

## Process + data semantics

| Limitation | Cause | Status |
|---|---|---|
| `refs.resolver_version` is set per-write; no lazy daemon-start re-resolution | Simplification for v1.2 | Planned if it becomes a pain |
| Daemon restart required when switching resolver versions (changes affect new files only) | No re-resolution trigger | Future |
| Index model-switch invalidates all embeddings (dimensions differ) | No per-model retention | **Removed in v5.0** — no embeddings to invalidate |
| `files.project_id` as a queryable scope | Single-project schema pre-v1.5 | **Shipped in v1.5** — nullable `project_id` on `files` with cascade delete; NULL = implicit root project, so v1.4 configs keep working |

## Planned for v5

| Feature | Notes |
|---|---|
| Rust language support | tree-sitter parser + use-tree resolver; planned v5 |
| Route literals as a symbol kind | `find_route(pattern)` MCP tool, opt-in via `route_constructors` config; planned v5 |

## Things you might expect but we don't claim

- **Type-perfect dynamic dispatch.** If code reassigns a function variable at
  runtime, static analysis can't follow it. Mycelium returns the static
  resolution; it's not a runtime tracer.
- **Call-site inlining.** We track symbols and refs, not inlined function
  bodies. A call to a tiny helper counts as a call, even when the compiler
  inlines it.
- **Documentation comments outside Godoc/JSDoc/PEP-257 form.** Our
  docstring extraction follows each language's convention.
- **Hover/completion.** Mycelium is a structured read-only index for
  agents; LSPs are a better fit for interactive IDE use.

## Maintenance

Edit this file when shipping a milestone or removing a limitation. Keep
the rows concise — a row that needs a paragraph probably wants a
CONTEXT.md non-goal instead.
