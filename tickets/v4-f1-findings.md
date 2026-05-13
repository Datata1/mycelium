# F1 findings — TypeScript field test (Codesphere monorepo-4)

**Status:** strong signal, telemetry-complete. The first v4-era field
test against an external TS codebase.
**Plan reference:** `~/.claude/plans/10-v4-agent-native-completed.md`
(v4 Phase 2).
**Goal:** validate v4 Phase 1 (B1+B2+B3) against a real non-mycelium
TS repo and surface the gaps that block v4.0 adoption.

## What this is

The original v4 Phase 2 plan named **Python/Django** as the F1
target. What actually happened was a real PR-review session against
**Codesphere/monorepo-4** (the same TS monorepo that produced the
v3.1 F1-F8 findings). The user installed myco, wired it into Claude
Code MCP, and asked the agent to address PR feedback on
`packages/ui-tests/src/utils/plans.ts`. ~30 min, real code work.

This **complements** rather than replaces the planned Python/Django
F1: it gives v4-era TS data (Phase 1 active, telemetry on, doctor
adoption section enabled). The Python/Django run still belongs in
the Phase 2 plan — but the bugs surfaced here are P0 enough that
they should land before Python/Django runs.

The test repo is the same `monorepo-4` reference codebase from the
v3.1 plan (49 workspace projects, ~3079 indexed files, 24647
symbols, 0.2% unresolved refs after v1.3 TS resolver — a healthy
index by every measure that v3.x targets).

## Setup

- Repo: `monorepo-4`, branch `jd/fix-ui-pc/plans`, 3079 indexed files
  across 49 configured workspace projects.
- `.mycelium.yml`: `telemetry.enabled: true`, projects fully
  enumerated, watcher.backend default (fsnotify), no exclude tweaks
  beyond defaults.
- Daemon running, MCP wired into Claude Code.
- Agent: Claude Code via MCP.

## The task

PR feedback on `packages/ui-tests/src/utils/plans.ts`:
- "sorting is better when getting the smallest plan, so not in `from`"
- "`resolve` is not explicit"
- "There are integrationtest utils for plans aswell and they do it
  better there, maybe there is something we can reuse"

Real refactor: rewrite `Plans` class to mirror the integration-test
`findPlan(opts)` API. Three iterations of user feedback, ending in
a `findPlan({cpu: number | 'smallest'})` API that drops the brittle
`Plan` string enum.

## Quantitative data

```
Session ses_bea123d6  duration 4m 24s  conversation turns 69
Total tool calls: 70
File edits:        22
myco calls:        10  (3 errored, 4 returned ~empty, 3 useful)
Fallback calls:    29 exploratory (+ 21 Edit + others)
```

Per-tool myco breakdown:

| tool | calls | ok | out_bytes | useful? |
|---|---|---|---|---|
| `find_symbol` | 1 | 1 | 14 B | **no** (returned null on `WorkspacePlan`) |
| `get_references` | 1 | 1 | 4 B | **no** (empty) |
| `get_file_outline` | 1 | 1 | 4 B | **no** (empty) |
| `search_lexical` | 1 | 1 | 4 B | **no** (returned null) |
| `read_focused` | 1 | **0** | 0 B | **no** (`too many open files`) |
| `impact_analysis` | 1 | **0** | 0 B | **no** (`symbol not found`) |
| `get_neighborhood` | 1 | **0** | 0 B | **no** (`symbol not found`) |
| `get_file_summary` | 1 | 1 | 124 B | yes |
| `list_files` | 1 | 1 | 309 B | yes |
| `ping` | 1 | 1 | 15 B | yes |

**Headline:** 7 of 10 myco calls returned no useful data. The agent
fell through to `Bash/grep` (6 calls, 6.0 KiB) and `Read` (20 calls,
100.7 KiB) for the rest of the session.

Adoption-doctor output on this repo:

```
[  ok ] search_lexical_only        search_lexical = 23% of myco calls (within band)
[warn] read_focused_under_used    read_focused = 10% of file reads (warn below 15%)
[  ok ] grep_over_myco             myco/grep ratio = 3.4 (within band)
```

Counterfactual cost output:

```
with myco (actual)        417.4 KiB  / 106,850 tokens
without myco (modelled)   417.1 KiB  / 106,768 tokens
estimated savings          -326 B   / -82 tokens   (-0.1%)
```

The negative savings number is itself a finding — see T5.

## Qualitative findings

### T1. `find_symbol` returns null on TS type aliases [strong]

**Evidence:** Agent called `find_symbol{name: "WorkspacePlan"}` on a
type defined in `packages/payment-service/common/lib/Product.d.ts`
and re-exported across the monorepo. Result: null. But the type is
referenced in dozens of files; grep finds it instantly.

**Hypothesis:** The TS parser doesn't index `.d.ts` declaration
files as symbol sources (or treats them with a kind that
`find_symbol` doesn't surface). Type aliases in modern TS codebases
live disproportionately in `.d.ts` files (third-party packages,
generated typings, ambient declarations). If true, this is a real
TS-adoption blocker.

**Verify:** check `internal/parser/typescript/walker.go` for `.d.ts`
handling; run `myco list_files --language typescript` and grep for
`.d.ts` to see if any are indexed at all.

**Severity:** P0. See `tickets/v4-bug-typescript-dts-not-indexed.md`.

### T2. `read_focused` fails with `too many open files` [strong]

**Evidence:** Direct OS error from `os.ReadFile(abs)` in
`internal/query/read.go`:

> `daemon: read /Users/codesphere/monorepo-4/.../plans.ts: open ...: too many open files`

**Hypothesis:** Daemon process hit `RLIMIT_NOFILE`. fsnotify
watchers consume one fd per directory; 3079 files across 49 packages
plausibly involve 1000+ directories. macOS default soft limit is
256, Linux user default is often 1024. v1.7's Watchman backend
(planned, never shipped) was supposed to address exactly this.

**Verify:** `lsof -p $(pgrep myco)` on the affected daemon → fd
count vs `ulimit -n`.

**Severity:** P0. See `tickets/v4-bug-fd-leak-large-repo.md`.

### T3. `search_lexical` returned null on regex with `|` alternation [medium]

**Evidence:** `search_lexical{pattern: "WorkspacePlan|plans",
path_contains: "integration", k: 20}` → null. The pattern is
syntactically valid Go regexp (per `internal/query/lexical.go:42`),
the file `packages/integration-tests/src/__tests__/utils.ts`
contains `WorkspacePlan` references, and `path_contains:
"integration"` should substring-match `packages/integration-tests/`.

**Hypothesis options (need verification):**
1. `path_contains` filter eliminated the candidate set (maybe it
   matches `integrations/` only, or interaction with workspace-mode
   project-relative paths). The v3.1.1 workspace-mode disk-read fix
   ([CHANGELOG]) addressed a similar shape; this might be a
   sibling case.
2. Regex actually didn't match because the file is in
   `packages/integration-tests/src/__tests__/` and the path filter
   only saw the project-relative form `src/__tests__/utils.ts` →
   "integration" substring is in the project name, not the
   project-relative path.

**Verify:** `myco query grep "WorkspacePlan" --path-contains
"integration"` directly + check `candidatePaths` logic in
`internal/query/lexical.go` against workspace-mode project paths.

**Severity:** P1 — adoption silently degrades when path filters
eliminate everything. Needs the v3.1 A1 hint envelope treatment
(`{matches: [], hint: "no candidate files match path_contains=X;
suggested: [...]}`).

### T4. `bench-counterfactual` on external repo: silent ERR/DRIFT [strong, expected]

**Evidence:** Bench produced ERRs for `read_focused`,
`get_file_outline`, `get_file_summary`, `impact_analysis`,
`get_neighborhood` because the corpus targets are mycelium-tuned
(`ComputeSessionCost`, `internal/telemetry/aggregate.go`). Other
rows showed huge DRIFT because `MycoBytes` was tiny (no matching
target).

**Status:** Documented in v4 B3 ticket as expected behaviour. But
the UX is bad: no clear "wrong corpus for this repo" message, just
silent DRIFT. The user has to read the ticket to understand.

**Fix:** Detect when ALL targets fail to resolve on the daemon side
(every row is ERR or `MycoBytes` < threshold) → exit with a single
clear message: "this corpus is mycelium-tuned; other repos need
adaptive corpus (v4.1+) or `--corpus-file`." The adaptive-corpus
deferral from B3 is now justified — F1 is asking for it.

**Severity:** P1 for v4 release UX. Tracked in B3's deferred queue.

### T5. Counterfactual model breaks when myco fails [strong]

**Evidence:** Session-export shows **-0.1% savings** even though
the agent did 70 tool calls in 4 minutes addressing real PR
feedback. The math:

```
myco calls returned ~798 bytes total (mostly empty/errors)
counterfactual ≈ 798 × multiplier ≈ low hundreds of bytes
fallback (Read+grep+Edit) = 416.6 KiB (paid in full)
WithoutMycoEstimateBytes = counterfactual + fallback ≈ fallback
EstimatedSavingsBytes = WithoutMyco - actual ≈ 0 (or negative)
```

The model assumes myco usage **replaces** grep usage. But when
myco fails and the agent grep's anyway, **both** costs hit the
totals. The model can't distinguish "myco saved a grep" from
"myco failed, agent grep'd instead, both got paid."

**Fix:** counterfactual contribution should be **gated on the
record's OK flag**. Per-tool: when `Record.OK == false`, skip the
counterfactual contribution AND treat the matching fallback bytes
as "would have been paid anyway." The model would then correctly
say "no savings on this session" instead of "myco actively cost
you bytes."

**Severity:** P1. Misleads adoption metrics on every session where
myco hits any error. In a real adoption study (multiple users on
multiple repos), some failure rate is the norm, so the bias is
ongoing.

### T6. CLI ergonomics: `myco find` and `myco search` don't exist [strong]

**Evidence:** User typed `myco find symbol WorkspacePlan` and
`myco search "WorkspacePlan|plans"` to debug T1+T3. Both:

> `error: unknown command "find" for "myco"`

The actual commands are `myco query find` and `myco query grep`.
Top-level `myco --help` doesn't suggest these — they're nested
under `query`.

**Hypothesis:** First-time-user reflex names — `find` and `search`
— are exactly what users (and agents) type. The current `query`
subcommand prefix is correct architecturally but adds friction
when debugging adoption issues from the shell.

**Fix options:**
1. Add top-level alias commands `myco find` → `myco query find`,
   `myco search` → `myco query grep`. Pure UX, no schema change.
2. Promote `query` subcommands to top-level (breaks current users
   of `myco query find`).
3. Add a `Did you mean: myco query find?` to cobra's unknown-
   command error path.

**Severity:** P2 — cosmetic but compounds T1's debugging cost.

### T7. Session export `Task` field shows IDE-annotation tag-soup [strong, cosmetic]

**Evidence:** Session export markdown:

```
> **Task:** <ide_opened_file>The user opened the file
> /Users/codesphere/monorepo-4/packages/ui-tests/src/utils/plans.ts in the
> IDE. This may or may not be related to the current task.</ide_opened_file>
```

The actual user prompt was "I got feedback on my PR regarding this
feature branch PR. You can check gh to see whats going on in the
PR. ..."

**Hypothesis:** `ParseTranscript` extracts the first user message;
when a session begins with an IDE-side tool injection, that
injection is what gets stored. The IDE `<ide_opened_file>` tag
shouldn't count as a user task.

**Fix:** Skip messages whose body starts with a known
ide-wrapper tag pattern; take the first **prose** user message
instead.

**Severity:** P3 — cosmetic but the user noticed it on the very
first export.

## What WORKED (don't lose this signal)

- **doctor on monorepo-4 ran clean.** 3079 files, 0.2% unresolved
  refs — the v1.3 TS resolver is genuinely solid on this codebase.
- **B2 adoption section produced real signal.** read_focused at
  10% (correctly flagged as warn, would have been invisible
  pre-B2), grep ratio 3.4 (within band), search_lexical 23% (within
  band). Exactly the data this feature was designed to surface.
- **list_files + get_file_summary worked first try.** The two myco
  calls that returned useful data.
- **i18n_json:6565** documents indexed. v3.3 documents surface
  shipping value.
- **3 workspace projects flagged as empty** by doctor's
  `projects_configured_but_empty` check — `custom-typings`,
  `migrations`, `streamy-browser` — likely a config issue in the
  repo's `.mycelium.yml` (these projects are typings-only / SQL-
  only / etc.). v3.1 A4's check is doing exactly the job it's
  meant for.

## What this means for v4 prioritisation

The Phase 1 work (B1, B2, B3) is **architecturally correct** but
**operationally insufficient for TS adoption** until T1+T2 land.
Concrete schedule revision:

1. **Before Phase 3:** ship the two P0 bugs (T1 dts-indexing, T2
   fd-leak). These are pure blockers on TS repos. New tickets:
   - `tickets/v4-bug-typescript-dts-not-indexed.md`
   - `tickets/v4-bug-fd-leak-large-repo.md`

2. **Before v4.0 release:** ship T5 (counterfactual error-aware) +
   T4 (adaptive corpus, vorgezogen aus B3 deferred queue). Both
   will make the v4.0 messaging honest.

3. **Phase 2 Python/Django (F1 original):** still belongs, but
   reschedule to **after** the P0 bugs land — otherwise we'll
   get the same null-return story on Python type aliases / large
   Django repos.

4. **F2 Rust/Axum:** unaffected — different parser surface.

## Action items

- [ ] Land `tickets/v4-bug-typescript-dts-not-indexed.md` (T1)
- [ ] Land `tickets/v4-bug-fd-leak-large-repo.md` (T2)
- [ ] Promote T5 (counterfactual error-aware) from "v4.1+" to "v4.0
      release blocker" — small change to `ComputeSessionCostFor`
- [ ] Promote T4 (adaptive corpus) from "v4.1+" to "v4.0 release
      blocker" — was in B3 deferred queue
- [ ] T3 (search_lexical path_contains): investigate during Python
      F1 prep — may be the same workspace-mode projection bug as
      v3.1.1
- [ ] T6 + T7: nice-to-have; defer to v4.1+ unless cheap

## Out of scope for this ticket

- **Implementing any fix.** F1 is observation. P0 tickets implement.
- **Re-running the session post-fix.** Worth doing once T1+T2 land,
  but separate ticket — Phase 2 re-run on the same repo with the
  bugs fixed gives the v4.0 acceptance number.
- **Multi-version Codesphere.** This run was on one branch + one
  task. Cross-task / cross-branch coverage is v5+ work.

## Honest caveats

- Single agent (Claude Code), single session. The behaviour of
  other agents (Cursor, Aider) under the same bugs may differ in
  ways that change which mode is most painful.
- `monorepo-4` is unusually well-typed (49 explicit projects, low
  unresolved-ref ratio) — the dts-indexing bug may be even more
  impactful on less-disciplined TS repos where everything routes
  through ambient declarations.
- The agent is the same agent the v3.1.2 priming was tuned for. A
  greenfield agent that hadn't read CLAUDE.md may have reached for
  myco less, which would have HIDDEN T1+T2 (you can't see a tool
  fail if you don't call it).
