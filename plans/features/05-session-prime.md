# WS05 — Session Prime (dynamic priming at session start)

Size: **M**. Depends on: nothing. Ships independently.

## Problem

The user had to remind the agent in chat to use myco at all. Static priming
exists (heavily editorialized tool descriptions in `pkg/mcpschema/tools.go`;
the CLAUDE.md snippet from `internal/wizard/claudemd.go:13-34`) and
demonstrably was not enough. What CLAUDE.md cannot carry is *freshness
proof* — "the index is alive, here, and covers this repo right now" — and
it only exists where the user accepted wizard step 7.

Decision (2026-07-04): add a **SessionStart hook** that injects a compact,
live priming block. SessionStart over UserPromptSubmit because it fires
once per session (token cost) and the UserPromptSubmit slot is already
taken by `myco session start --auto` (`cmd/myco/cmd_session.go:436`). The
CLAUDE.md snippet stays — stable rules there, live proof here.

## Mechanism

1. New subcommand `myco session prime` (`cmd/myco/cmd_session.go`,
   `newSessionPrimeCmd`). Fetches Stats via the existing `callRead`
   daemon-up/daemon-down fallback helper (pattern at `cmd_query.go:191`),
   then prints the Claude Code hook contract:

   ```json
   {"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"<text>"}}
   ```

2. Priming text builder as a pure function (unit-testable), budget
   **≤ 250 tokens**:

   ```
   myco (MCP) is indexing this repo: 156 files (go 155), 1,057 symbols,
   refs 96% resolved, last scan 2m ago. Rules: identifier → find_symbol
   (never search_lexical); callers → get_references; read a file →
   read_focused(path, focus=...); orientation → get_file_outline /
   get_file_summary; blast radius → impact_analysis. search_lexical is
   ONLY for literal strings/regex.
   ```

   Include last-scan age so a stale index is visible rather than bragged
   over.

3. **Failure behavior: any error (no index, daemon down, no DB) → print
   nothing, exit 0.** A hook that breaks sessions kills adoption of the
   whole product.

4. Hook installation: extend `installSessionHooks`
   (`cmd_session.go:396-456`) with a `SessionStart` entry via the existing
   idempotent `mergeHookList`; update the printed summary. Add the same
   hook to the wizard's hook-install step so `myco init` users get it.

## Risks

- Claude Code hook contract drift → plain stdout from a SessionStart hook
  is also treated as context, so a JSON-shape mismatch degrades gracefully.
- Token cost per session → hard budget assert in the unit test.
- Duplicate context when the CLAUDE.md snippet is also installed → both are
  short; the hook text carries the live stats, the snippet the durable
  rules. Acceptable overlap.

## Tests

- Unit: fixed Stats in → exact string out; assert char/token budget.
- Integration: valid hook JSON on stdout, exit 0; with no index present →
  empty output and exit 0.
