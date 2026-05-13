# B2 — Adoption-health doctor checks

**Priority:** P1 for v4 Phase 1
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Depends on:** v3.4 Block A (telemetry + session export — already shipped)

## Goal

Surface the three documented adoption failure modes in
`docs/adoption.md` automatically, so users notice their agent is in
the "search_lexical only" / "Read instead of read_focused" /
"grep instead of myco" pattern *before* manually exporting a session
and squinting at the table.

`myco doctor` reads recent session telemetry + external logs and
prints a per-failure-mode warning when the ratio crosses a threshold.
Closes the G5 finding ("telemetry-off was a real adoption defect")
by making the data the user actually wants visible without extra
commands.

## What it computes

For sessions in the last `--window` (default 7 days):

```
adoption health (last 7 days, 12 sessions)
  search_lexical-only pattern:    OK    (28% of myco calls — within band)
  read_focused vs Read ratio:     WARN  (4% — agent prefers full Read; expected ≥ 30%)
  myco vs Bash/grep ratio:        WARN  (1.4 myco / grep — expected ≥ 3.0)
  read_focused without focus:     OK    (0% — see B1)
```

Each warning links to the relevant `docs/adoption.md` section so the
user knows what to do.

## What it changes

- New `internal/doctor/adoption.go` — pure function from session
  summaries to a list of `AdoptionFinding{level, message, hint}`.
- `internal/doctor/doctor.go` — calls the adoption check after the
  existing checks; tags it as a separate section in the output.
- `cmd/myco/main.go` `doctor` subcommand: `--window <duration>` flag
  (default `168h` = 7 days), `--no-adoption` to suppress.
- `internal/doctor/checks.go` (or wherever existing checks live) —
  add `telemetry_dark_spot`-style toleration when telemetry has
  fewer than `--min-sessions` sessions in window (default 3) so a
  fresh repo doesn't false-positive.

## Critical files

- `internal/doctor/` — read the existing pattern (see
  `telemetry_dark_spot` from v3.4 G5, which is already in tree).
- `internal/telemetry/aggregate.go` — has `Aggregate(path)`,
  `ListSessions`, `ComputeSessionCost`. New helper
  `AggregateRecent(path, since time.Time) ([]Summary, error)` may
  be useful (or compute window-filter in the doctor caller).
- `internal/telemetry/external.go` — has `SummarizeExternal` and
  `TotalExploratory` for the fallback side.
- `docs/adoption.md` — the canonical failure-mode catalogue. Each
  finding's hint should reference the relevant section.

## Acceptance criteria

- `task check` passes. New unit tests for `internal/doctor/adoption.go`
  covering each failure mode + the "not enough data" gate.
- `myco doctor` on a fresh repo (no sessions) prints "adoption
  health: no telemetry yet (need ≥ 3 sessions)" and continues.
- `myco doctor` on the mycelium-self repo (lots of sessions) prints
  the four-row block above; values vary by session.
- Each WARN line includes a one-line hint and a `docs/adoption.md`
  pointer.
- `myco doctor --no-adoption` skips the new section.
- Exit code: doctor exits non-zero only when an existing check
  fails — adoption WARN does **not** fail doctor (informational,
  not gate). Document this clearly in the output.

## Failure-mode definitions (pin in adoption.go)

| Mode | Metric | OK threshold | WARN threshold |
|---|---|---|---|
| `search_lexical_only` | `search_lexical / total_myco_calls` | ≤ 50% | > 70% |
| `read_focused_under_used` | `read_focused / (read_focused + Read)` | ≥ 30% | < 15% |
| `grep_over_myco` | `myco_calls / Bash/grep_calls` | ≥ 3.0 | < 1.5 |
| `read_focused_without_focus` | post-B1: count of read_focused calls with empty focus param | 0 | ≥ 1 |

Thresholds are calibrated against the v3.4 mycelium-self-index
data (16-session aggregate) — re-tune after B2 lands and the user
runs it on monorepo-4 / Django field test data.

## What this enables

- **Self-driving adoption.** Users don't need to read
  `docs/adoption.md` to notice they're in a failure mode; doctor
  tells them.
- **A/B comparison by date.** `--window 24h` vs `--window 168h`
  shows whether a CLAUDE.md change moved the needle.
- **Foundation for v4.1+ "myco coach"** — one possible v4.1
  direction is interactive remediation hints. B2 gives the data
  surface to drive that.

## Out of scope

- **Reading the agent's transcript** to figure out *why* a call
  was made. That requires Claude Code transcript parsing
  (already in `internal/telemetry/transcript.go`) but composing it
  with the adoption check is a v4.1+ idea.
- **Per-tool drill-down.** Doctor stays summary-level. The
  `myco session export` markdown form already has the per-tool
  breakdown.
- **Auto-fixing** — doctor reports, doesn't mutate config or
  CLAUDE.md.

## Honest caveats

- Thresholds are guesses anchored to one repo's data. The first
  user to run B2 on a different repo will likely find the
  thresholds wrong; document this in the output ("thresholds
  tuned against mycelium-self; PR welcome").
- Adoption checks need ≥ 3 sessions to fire. A user who runs
  `myco doctor` immediately after `myco init` sees the "no
  telemetry yet" line — that's correct behaviour, not a bug.
