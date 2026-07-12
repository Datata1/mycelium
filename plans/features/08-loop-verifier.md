# WS08 — myco as loop verifier (`myco check`, `select_tests`, Stop-hook gate)

Size: **L**. Depends on: nothing. Ships in three stages, each alone.

## Problem

Loop engineering (mid-2026) reframes agent work as trigger → action →
**verifier** → stop rules. The loop bottleneck on large codebases is
verification latency: a full type-check + test run costs minutes *per
iteration*. myco holds the one asset that can shortcut this — an
always-fresh reference graph that knows in milliseconds what the compiler
only discovers minutes later. Today myco has no verifier surface at all:
nothing answers "did my edit structurally break callers I didn't touch?",
nothing selects the tests worth running, and nothing gates a session on
structural health.

## Mechanism

Three deliverables (user decisions 2026-07-12: blocking gate opt-in;
check also exposed as MCP tool; full A+B+C scope):

**A. `myco check [--since <ref>]` + MCP tool `verify_changes`.**
Base = `git merge-base <ref> HEAD` (ref defaults to HEAD), changed set =
`git diff --name-only --no-renames <base>` (includes uncommitted tracked
changes — deliberately different from `ResolveSince`'s committed-only
three-dot form, which stays untouched). Old blobs come from
`git show <base>:<path>` and are parsed with the same
`parser.Registry` that built the index (`Parse` is content-based).
Removed = old qualified names absent from the current symbols of the whole
changed set AND not defined anywhere else in the index (existence guard
kills move/duplicate false positives). Dangling refs are matched only from
files *outside* the diff: exact `dst_name` match ⇒ FAIL; `dst_short` match
not resolved to a still-existing symbol ⇒ WARN; short match resolved to a
different live symbol ⇒ ignored. Four checks, doctor-style levels/exit
codes (0/1/2): `git_scope`, `index_fresh_for_changes` (exact os.Stat over
the ≤500 changed paths; daemon-up pre-gates a reconcile via the dispatcher,
daemon-down FAILs with remediation), `parse_old_versions` (warn-only),
`removed_but_referenced`.

Honest limit, stated in the tool description: the TS resolver is
heuristic — this catches broken *named* references before the compiler;
it does not replace `tsc`, it runs in front of it.

**B. MCP tool `select_tests` + CLI `myco tests`.**
Same changed-set mechanics → `SymbolsInFiles` seeds → new multi-seed
inbound-closure CTE (`InboundClosureFiles`, seeds chunked at 500, depth
default 5 / cap 10) → filter through `check.IsTestFile` (path conventions:
`*_test.go`; `.test.`/`.spec.` infix + `__tests__` segment; `test_*.py` /
`*_test.py` + `tests` segment; `testdata` always excluded) → distance-ranked
file list, one per line (pipe-able into a test runner). Changed test files
rank at distance 0. Also fixes the false `kind="test"` claim in the
`impact_analysis` tool description.

**C. `myco session verify` — opt-in Stop-hook gate.**
`myco session hooks install --verify-gate` merges a Stop entry running
`myco session verify` (before annotate). The hook honors
`stop_hook_active` (early exit 0), runs `verify_changes{Since:"HEAD"}`
under a 10s timeout, and blocks — `{"decision":"block","reason":…}` on
stdout, exit 0 — **only** on a `removed_but_referenced` FAIL. A stale
index never blocks (infra problem, not the agent's code); warnings never
block; any internal error is silent exit 0 (the `session prime` contract).
Loop safety = `stop_hook_active` guard + Claude Code's 8-block cap.

## Layering

New package `internal/check` (pure diff/classify + `IsTestFile`);
orchestration in `service.VerifyChanges` / `service.SelectTests` so
daemon-up, daemon-down and MCP share one code path. The parser registry is
injected via `Service.SetParsers` (mirrors `SetProbe`; parsers only, never
resolvers — no go/packages load). DB stays read-only; the daemon's
stale-path reconcile reuses the existing reindex bypass.

## Risks

- False FAILs on unusual qualified-name collisions → mitigated by the
  index-wide existence guard and by classifying short-name-only evidence
  as WARN, never FAIL.
- A blocking hook that misfires kills adoption → gate is opt-in, blocks
  on exactly one high-confidence check, silent on every error path.
- >500 changed paths exceed the IN-clause cap → surfaced as an error
  ("pick a tighter base ref"), never truncated.

## Tests

- gitref worktree helpers against a scripted temp git repo (base
  resolution, uncommitted visibility, delete/rename splitting,
  missing-at-base).
- Table-driven `internal/check` unit tests (move-within-diff, duplicate
  qualified, exact-vs-short classification, warn/fail rollup);
  `IsTestFile` conventions.
- Integration: `test/integration/verify_test.go` (fail path names the
  dangling call site; clean deletion passes; stale-index fail) and
  `select_tests` (change `a.go` ⇒ exactly `b_test.go`, never the
  unrelated `c_test.go`).
- Goldens for both new renderers; `TestToolParity` covers the
  registry/schema/ipc lockstep automatically.
- Stop-hook unit tests: fail blocks, stale fail does not, warn does not,
  `stop_hook_active` short-circuits; hook install idempotency with and
  without `--verify-gate`.

## Status

- Planned 2026-07-12.
