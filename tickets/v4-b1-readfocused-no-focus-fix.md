# B1 — `read_focused` fix when `focus` is empty

**Priority:** P1 for v4 Phase 1 — closes the v3.4 A3 G2 finding
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Depends on:** none (load-bearing for v4 ethos but standalone)

## Goal

Stop `read_focused` from being net-negative when the agent calls it
without a `focus` argument. The v3.4 `bench-counterfactual` measured
14 KiB of myco output for a 12 KiB file — the JSON envelope + line
markers cost more than they save. The model now reflects that
honestly (multiplier dropped from 2.0× to 1.0×) but the **tool**
still encourages the wrong call shape.

This ticket fixes the tool side: `read_focused` without `focus`
returns a *useful subset* (outline + first N lines + a hint to pass
focus), not the full file. The agent's reflex of "I have a path,
let me read it" gets a graceful answer that's actually cheaper than
`Read`, and the savings number stops being a lie when myco is used
wrong.

## What changes

For a call like `read_focused{path: "internal/telemetry/aggregate.go"}`
with no `focus`:

- **Today:** returns the full file rendered with non-matching symbols
  collapsed (which is the same as no collapsing because nothing
  matches), plus envelope. Net heavier than `Read`.
- **After:** returns a compact preview:
  ```
  <file outline — 10 symbols, 280 lines>
  // first 50 lines verbatim
  package telemetry
  ...
  // <hint>
  // To read more, pass `focus=<query>` — e.g. focus="ComputeSessionCost"
  // returns the function body + its callers in this file. Use
  // get_file_outline for symbol-only listing.
  ```

When `focus` IS set, behaviour is unchanged.

## Critical files

- `internal/focus/focus.go` (or wherever the focus rendering lives —
  grep for `ReadFocused` in `internal/focus/`).
- `internal/query/read.go` — the `ReadFocused` query method needs to
  branch on empty focus.
- `pkg/mcpschema/tools.go` — update `read_focused` description to
  document the new no-focus behaviour.
- `internal/telemetry/counterfactual.go` — leave the multiplier at
  1.0 for now; once B1 is in production for a session-cycle, re-run
  `myco bench-counterfactual` and adjust if the new no-focus shape
  measures meaningfully different.

## Acceptance criteria

- `task check` passes.
- `myco read internal/telemetry/aggregate.go` (CLI form, no focus
  arg) returns the preview shape, not the full file.
- `myco read internal/telemetry/aggregate.go --focus
  ComputeSessionCost` is unchanged from current behaviour.
- New test in `internal/query/read_test.go`:
  - empty focus → output bytes < `wc -c` of the source file
  - empty focus → output contains the outline summary line and the
    hint string
  - non-empty focus → output unchanged from baseline (snapshot)
- `myco bench-counterfactual` shows `read_focused` measured ratio
  drops below 1.0× (myco is now genuinely lighter than Read) — if
  it does, lower the multiplier in `counterfactualModel` and the
  pinned `calibration_test.go` value in the same commit.

## What this enables

The v3.4 A3 honesty story closes: the savings number reflects a
tool that's *always* a saving, not a tool that's a saving only when
used right. The G2 adoption-fixed-point finding stops dragging on
adoption metrics. Future agents that don't read CLAUDE.md still pay
a smaller penalty for the wrong reflex.

## Out of scope

- **Re-tuning the focus algorithm** (the symbol-collapse logic
  itself). This ticket only changes what happens when `focus` is
  empty; the existing focus-set rendering is fine.
- **Auto-deriving a default focus** from the file path or recent
  agent context. That's a v4.1+ idea — explicit `focus=` is still
  the load-bearing signal.
- **Removing `read_focused` entirely** in favour of a different
  surface. The tool stays; only its no-focus behaviour changes.

## Honest caveats

- The "first 50 lines + outline + hint" shape is a guess. Once B1
  is in production, the bench number tells us whether 50 was the
  right cut or whether 30 / 80 is better. Acceptable to tweak in a
  follow-up commit; document the chosen N in `read.go`.
- Some agents may interpret the hint as "read_focused isn't useful
  without focus" and stop calling it entirely. That's *also a fix*
  for the original problem — a `Read` call costs the same bytes
  and doesn't lie about being a saving. Worth watching adoption
  metrics post-B1.
