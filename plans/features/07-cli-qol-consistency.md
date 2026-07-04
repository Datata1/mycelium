# WS07 — CLI QoL & Doc/Code Consistency

Size: **S**. Depends on: nothing (mention `session prime` in docs only once
WS05 lands). Each item ships alone.

## Problem

Small inconsistencies that erode trust in the docs and leave holes in the
CLI surface:

- `docs/adoption.md:31` and `CLAUDE.md:123` document `myco init --mcp
  claude`, but that flag form no longer exists — `init` is an interactive
  wizard (`cmd/myco/cmd_init.go`; MCP registration is step 6, the CLAUDE.md
  priming snippet step 7).
- `internal/doctor/adoption.go:146` counts a `get_definition` tool in its
  structural-tools map; that tool no longer exists.
- The CLI covers 11 of 12 tools — `find_document_key` has no CLI command.
- Only `find` and `search` got top-level aliases (v4 T6: "agents typed the
  reflexive name and it errored"); the same reflex applies to
  `refs`/`outline`/`impact`, still buried under `myco query`.

## Mechanism

1. **doctor ghost**: delete the `"get_definition"` entry from
   `structuralTools` in `internal/doctor/adoption.go:146`. Keep the
   structural set graph-tools-only (that is the metric's meaning) — do not
   add outline/summary. Update `adoption_test.go`.
2. **stale docs**: rewrite the `myco init --mcp claude` references in
   `docs/adoption.md` (lines 31, 135) and `CLAUDE.md:123` to describe the
   actual flow (interactive `myco init`; wizard steps 6/7;
   `myco session hooks install`). Mention `session prime` once WS05 lands.
3. **dockey CLI parity**: add `myco query dockey <key>` (flags `--kind
   --project --limit`) in `cmd/myco/cmd_query.go`, following the
   `runQueryRefs` shape over the generic `callRead` helper.
4. **top-level aliases**: `myco refs`, `myco outline`, `myco impact` — thin
   cobra wrappers delegating to the existing `runQuery*` functions
   (pattern: `cmd_query.go:206-252`). Aliases lower the human-verification
   cost, which is how users discover what to tell agents.

## Optional extras (each S, take or leave)

- **doctor check "priming installed?"**: inspect
  `.claude/settings.local.json` for the SessionStart prime hook (WS05) and
  CLAUDE.md for the priming marker (`internal/wizard/claudemd.go:38`);
  warn with the exact install commands. Closes the loop: doctor currently
  diagnoses adoption failure but can't tell you the two switches are off.
- **docs: measuring nudge efficacy**: a short docs/adoption.md section on
  comparing pre/post-upgrade sessions with `myco session compare`
  (fallback_exploratory delta) — no code, enables re-tuning the
  `doctor/adoption.go` thresholds later.

## Risks

None meaningful; all items are additive or doc-only.

## Tests

- `adoption_test.go` update for the ghost removal.
- CLI integration case for `query dockey` and one alias (equivalence with
  the `query`-prefixed form).
