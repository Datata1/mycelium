# Features Roadmap — Freshness & Adoption

Seven workstreams, one plan document each. Goal: make the index trustworthy
enough that agents never need a grep fallback, and make every tool result
teach the next call so the full 12-tool surface actually gets used.

Sizes: **S** < 1 day, **M** 1–3 days, **L** 3–5+ days.

## Motivating observations (field use, 2026-07)

1. **Adoption**: the agent had to be reminded in chat to use myco at all —
   static priming (tool descriptions, CLAUDE.md snippet) demonstrably wasn't
   enough on its own.
2. **Freshness**: files were sometimes missing from the index; the agent got
   an empty answer, fell back to bash, and found the file — trust destroyed.
   Root causes are branch switches (no post-checkout hook, `.git` unwatched),
   swallowed watcher-overflow errors, and a catch-up scan that never prunes.
3. **Tool breadth**: essentially only `search_lexical` and `find_symbol` get
   used; the other ten tools almost never. No runtime result ever suggests a
   follow-up tool, misses return bare "no matches" strings, and `read_focused`
   dumps raw JSON.

Freshness comes first (user decision, 2026-07-04): as long as the index gives
empty answers for files that exist, every adoption nudge is wasted.

## Execution waves

| Wave | Workstreams | Rationale |
|------|-------------|-----------|
| 1 | 01, 02 | Trust first: make every reindex trigger a full self-heal, then add the triggers (hooks, overflow rescan, HEAD watch). |
| 2 | 03, 04, 05 | Adoption: results explain misses and teach the next tool; session priming becomes automatic. |
| 3 | 06, 07 | Polish: MCP annotations, CLI parity, doc consistency. Independent, any time. |

## Dependency graph

```
01 reconcile-and-hooks ─► 02 watcher-hardening   (rescan trigger target, index_meta)
01 reconcile-and-hooks ─► 03 why-empty-hints     (last_full_scan_at staleness hint)
04 renderer-nudges ──────► 03 why-empty-hints    (soft: same renderer files; 04 first avoids churn)
05 session-prime ─────────────────────────────── independent
06 mcp-annotations ───────────────────────────── independent
07 cli-qol-consistency ───────────────────────── independent
```

## Workstreams

| # | Plan | Size | Depends on | Blocks |
|---|------|------|-----------|--------|
| 01 | [reconcile-and-hooks](01-reconcile-and-hooks.md) — prune pass in RunOnce, `index_meta`, post-checkout/merge/rewrite hooks | M/L | — | 02, 03 |
| 02 | [watcher-hardening](02-watcher-hardening.md) — overflow → rescan, `.git/HEAD` watch, doctor freshness check | M | 01 | — |
| 03 | [why-empty-hints](03-why-empty-hints.md) — FSProbe path diagnosis, Hints envelopes for get_references/search_lexical, richer empty texts | L | 01 | — |
| 04 | [renderer-nudges](04-renderer-nudges.md) — runtime "next:" nudges, real read_focused renderer, refs/stats parity | M | — | 03 (soft) |
| 05 | [session-prime](05-session-prime.md) — `myco session prime` SessionStart hook injecting live index context | M | — | — |
| 06 | [mcp-annotations](06-mcp-annotations.md) — tool annotations (readOnlyHint/title), protocol version negotiation | S/M | — | — |
| 07 | [cli-qol-consistency](07-cli-qol-consistency.md) — dockey CLI parity, top-level aliases, stale-doc fixes | S | — | — |

## Status (2026-07-04)

- **01 part A implemented**: prune pass in `RunOnce` (`PruneFilesExcept`,
  walk-error guard, `runMu`, `Report.FilesPruned`), migration
  `0010_index_meta.sql` + `last_full_scan_at`, reader
  `query.Reader.LastFullScanAt`. Verified end-to-end: branch switch with a
  deleted file → next `myco index` prunes the ghost row; walk failure
  aborts before the prune.
- **01 part B implemented**: table-driven `hook.ManagedHooks`
  (post-commit/checkout/merge/rewrite), `InstallAll`/`UninstallAll`/`Run`,
  `myco hook <name>` subcommands, wizard installs all four +
  `core.hooksPath` warning, uninstall restores backups. Verified
  end-to-end: `myco init -y` installs all four, real `git checkout`
  runs the hook cleanly, foreign hook backed up and restored.
- **02 implemented**: Overflow events (fsnotify errors, burst ≥
  `watcher.rescan_threshold`, watchman fresh instance) → daemon rescan
  channel → `RunOnce`; `.git/HEAD` watch (worktree-aware) as zero-config
  checkout trigger; doctor `index_freshness` sampled check +
  `--deep` exact walk diff (`query.SampleFiles`/`AllFilePaths`).
- **05 implemented**: `myco session prime` (SessionStart hook body,
  silent-on-error), wired into `session hooks install` + wizard;
  `ipc.Stats.LastFullScan` carries the reconcile age onto the wire.
- **04 implemented**: "next:" nudges on find_symbol/get_references,
  definition-note on search_lexical (regex table, unit-tested), real
  `FocusedRead` renderer (RawJSON retired), references resolved/DstName
  parity, stats renderer expanded (+ empty-index lead). Golden fixtures
  regenerated; new fixtures for read_focused ×3, definition note,
  empty stats.
- **03 implemented**: `FSProbe` path diagnosis (`Service.SetProbe`,
  wired daemon + CLI), `{matches, hints}` envelopes for
  get_references/search_lexical (breaking wire change, CHANGELOG'd),
  not-found errors with probe diagnosis on read_focused/outline/summary,
  find_symbol never-reconciled hint, static empty-text upgrades.
- 06, 07: not started.

## Decisions already made (repo owner, 2026-07-04)

- **SessionStart prime hook: yes** — CLAUDE.md priming alone was not enough;
  the hook injects live index stats + tool rules at session start.
- **No .gitignore parsing.** Query-time "why is this file not indexed"
  hints (workstream 03) are the answer; the walker stays config-glob driven.
- **Freshness before adoption** in execution order.

## Non-goals (unchanged from docs/limitations.md)

- No natural-language `ask()` tool, no LLM summaries at index time, no
  cross-repo federation.
- No `structuredContent` dual output in MCP responses (doubles token cost,
  no adoption gain).
- Rust support and `find_route` stay on their own track; not part of this
  roadmap.

## Non-negotiable constraints (apply to every workstream)

- Daemon stays the only SQLite writer; `internal/query` the only reader,
  `internal/pipeline` the only writer; transports never issue raw SQL.
- Migrations are additive only — new numbered file per schema change.
- No pre-commit hook ever; post-* hooks only.
- Every build/test/lint command carries `-tags sqlite_fts5`.
- Every new query method gets an integration test case; benchmarks guard the
  query hot path.
- Renderer changes regenerate the golden fixtures
  (`go test ./internal/registry -run Golden -update -tags sqlite_fts5`);
  `TestToolParity` keeps mcpschema/registry/ipc in lockstep.
- Every workstream ends with a CHANGELOG entry.
