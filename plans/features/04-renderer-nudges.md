# WS04 — Renderer Nudges & Renderer Fixes

Size: **M**. Depends on: nothing (pure renderer work + golden updates; no
wire changes). Ships independently. Do before WS03 to avoid churn in the
same files.

## Problem

Agents use only `find_symbol` and `search_lexical`. All cross-tool guidance
is static (tool descriptions, CLAUDE.md) — visible once at tool-discovery
time, never at the moment of use. **No runtime result ever suggests a
follow-up tool.** And the results themselves undersell the product:

- `read_focused` is the only tool with no dedicated renderer —
  `internal/registry/registry.go:59` binds `render.RawJSON`, dumping the
  whole `Content` as an escaped JSON blob. Worst UX for the most
  token-heavy tool; one bad experience teaches the agent never to call it
  again.
- The references renderer (`render/refs.go:11`) drops `Resolved` and
  `DstName`, which the CLI shows (`cmd/myco/cmd_query.go:298-306`) and the
  tool description explicitly promises.
- The stats renderer (`render/stats.go:12`) drops ~20 struct fields
  (UnresolvedRatio, refs breakdown, documents_by_kind).

Decision: **nudge logic lives in the renderers** — they are pure functions
over the ipc DTOs, which already carry everything a nudge needs (qualified
names, paths); the MCP text surface is the only surface agents see; no wire
change; golden tests give determinism for free. Query-layer hints (WS03)
stay reserved for things needing DB/disk context.

## A. Follow-up nudges

New `internal/mcp/render/next.go`: one helper producing exactly **one**
trailing line, deterministically derived from the **first** hit only,
~25–40 tokens.

- `FindSymbol` (`render/symbols.go`): after matches, append
  `next: get_references("<Qualified>") · get_neighborhood("<Qualified>") ·
  read_focused("<Path>", focus="<Name>")`. Highest-leverage single nudge:
  find_symbol is one of the two tools agents already use, so it becomes the
  on-ramp to the other ten.
- `Lexical` (`render/search.go`): definition-line heuristic over snippets —
  regex table for common definition shapes (`^func\s+(\w+)`,
  `^\s*def\s+(\w+)`, `^(?:export\s+)?(?:class|interface|type)\s+(\w+)`,
  const/var decls). If a hit matches, append
  `note: "<Name>" looks like a symbol definition — find_symbol("<Name>")
  gives the definition; get_references("<Name>") lists callers.` First
  matching hit only. This attacks the myco-as-grep failure mode at the
  exact moment it happens.
- `References` (`render/refs.go`): append
  `next: read_focused("<SrcPath>", focus="<DstName>") ·
  impact_analysis("<DstName>") for transitive callers`.
- **No** nudge on outline/summary/neighborhood/impact/critical_path — they
  are already "deep" tools, and nudging everywhere devalues the signal and
  burns tokens.

## B. Dedicated `read_focused` renderer (kill RawJSON)

New `internal/mcp/render/read.go` `FocusedRead(raw json.RawMessage) string`
(the `ipc.FocusedRead` DTO at `results.go:267` has everything: Path, Focus,
Content, Stats, Hint, Expanded):

```
internal/auth/service.go  (focus: "Login")  expanded 2/9 symbols  1.2/6.1 KB
<Content verbatim>
---
expanded: AuthService.Login :42-78 · AuthService.Logout :80-90
hint: <Hint verbatim when present>
```

Rebind `internal/registry/registry.go:59` from `render.RawJSON` to
`render.FocusedRead`. Content passes through verbatim — no information
loss.

## C. Renderer parity

- `render/refs.go References`: add the resolved/textual tag and DstName,
  matching the CLI format — e.g. `%-30s  %s:%d  [resolved] -> <DstName>`.
- `render/stats.go Stats`: add `unresolved_ratio` (method exists:
  `Stats.UnresolvedRatio()`), the refs breakdown
  (`RefsTrulyUnresolved`/`RefsExternalKnown`/`RefsTypeResolved`),
  `documents_by_kind`, and last-scan age. Keep map iteration sorted (golden
  constraint). If `Files == 0`, lead with
  `index is empty — run myco index`.

## Risks

- Token creep → exactly one nudge line, first hit only, hard char budget in
  the helper.
- Golden churn → expected and cheap
  (`go test ./internal/registry -run Golden -update -tags sqlite_fts5`).
- Lexical heuristic false positives → `note:` phrasing is advisory and the
  regexes anchor on definition keywords.
- Nudge fatigue if WS03 hints and nudges stack → nudges only on non-empty
  results, hints only on misses; the two never co-occur.

## Tests

- Golden fixture regen for FindSymbol/Lexical/References/Stats; new
  fixtures for FocusedRead (focused / preview-with-hint / zero-expanded).
- Pure unit test for the definition-line regex table (mirroring
  `hints_test.go` style).
