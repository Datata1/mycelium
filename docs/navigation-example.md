# Navigation example: answering an architecture question with only `Read`

This is a recorded trace of an agent (Claude Code, 2026-04-26) answering an
architecture question about mycelium using **only the `Read` tool against
`.mycelium/skills/`**, no MCP queries. It exists to make the v2.3 skills tree
falsifiable: if these steps don't reproduce on your machine, the tree isn't
doing its job. The full-CI agent fixture remains a v3.0 acceptance criterion;
this doc is a sanity check, not the final measurement.

## Question

> Which package is the only reader of the SQLite index, and what are the
> main read entry points?

## Trace

**Step 1.** `Read .mycelium/skills/INDEX.md` — the entry point an agent
reaches first. The Packages table lists 28 directories with their language,
file count, and symbol count. Two names plausibly match the question:
`internal/query` (10 files, 71 symbols) and `internal/index` (7 files, 42
symbols). The descriptions are generic placeholders in v2.3 ("Package X
(N files, M top-level symbols).") so they don't disambiguate yet — that's
a known v2.3 limitation. We pick `internal/query` first because the name
is suggestive of the read role.

**Step 2.** `Read .mycelium/skills/packages/internal/query/SKILL.md`. The
top-level symbols section is conclusive:

- A `Reader` struct (`internal/query/query.go:15`).
- A `NewReader(*sql.DB) *Reader` constructor.
- Methods on `*Reader`: `FindSymbol`, `GetReferences`, `GetFileOutline`,
  `GetFileSummary`, `GetNeighborhood`, `ImpactAnalysis`, `CriticalPath`,
  `SearchLexical`, `EmbeddingStatus`, `Stats`. Every one is a read operation.

The "Top inbound" table reinforces it: callers come from `cmd/myco` (26
refs), `internal/daemon` (12), `internal/pipeline` (7), `internal/doctor`
(3) — exactly the boundary surfaces (CLI, daemon transports, pipeline
status, health checks) that need read access. No write callers.

**Step 3 (optional cross-check).** `Read .mycelium/skills/packages/internal/index/SKILL.md`
to confirm `internal/index` is *not* the reader. The package's top-level
symbols include `Open`, `OpenWithExtension`, `Close`, `DB()` — schema /
connection management — and migration helpers. No `Find*` / `Get*` /
`Search*` entry points. So `internal/index` owns the SQLite handle;
`internal/query` reads through it.

## Answer (with citations)

`internal/query` is the sole reader of the SQLite index. Main entry
points:

- `(*Reader).FindSymbol` — symbol lookup by name/kind, with project
  and `--since` scoping.
- `(*Reader).GetReferences` — call/import/type-use sites for a symbol.
- `(*Reader).GetNeighborhood` — local call graph around a seed.
- `(*Reader).ImpactAnalysis` / `CriticalPath` — graph-native v1.6 tools.
- `(*Reader).SearchLexical` / `Searcher.SearchSemantic` — string and
  embedding-based search.
- `(*Reader).GetFileOutline` / `GetFileSummary` / `Stats` — orientation
  helpers.

`internal/index` owns the `*sql.DB` and migrations; `internal/query`
borrows it via `NewReader(db)`.

## Reproduction notes

```bash
go build -tags sqlite_fts5 -o /tmp/myco ./cmd/myco
/tmp/myco index                       # if the index isn't current
/tmp/myco skills compile              # writes .mycelium/skills/
# Then Read INDEX.md and follow the trace above.
```

Tree size on the mycelium self-index (2026-04-26): 28 SKILL.md +
4 aspect INDEX.md + 1 root INDEX.md = 33 files, ~1.3 KLOC total
markdown. Largest single SKILL.md is internal/query at 157 lines.

## Known limitations exercised by this trace

- **Generic descriptions.** v2.3 SKILL.md `description:` is synthesized
  from file/symbol counts. A human-meaningful one-liner needs package
  docstring extraction, planned for v2.5+.
- **No deep-link to source line.** SKILL.md cites `file:line` for each
  symbol but the agent has to open the source file to read the body.
  This is by design: the lean / complementary contract.
- **Inbound table is directory-aggregated.** "cmd/myco — 26 refs" tells
  you cmd/myco depends heavily on query, but not which symbols.
  `myco query refs <symbol>` is the next step.
