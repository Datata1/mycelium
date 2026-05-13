# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project adheres
to [Semantic Versioning](https://semver.org/).

## [Unreleased]

Accumulated work since v2.0.0-rc1. Organised into named milestones
below so the chronology survives the still-open release tag. Tags
will be cut once the v3.4 prerequisite field test on a non-TS,
non-Go codebase lands (Python/Django, Rust/Axum, or Java/Spring
candidates — see `tickets/v3.4-non-ts-field-test-findings.md` for
status). Inside each milestone, the usual Keep-a-Changelog
categories (Added / Changed / Fixed / Measured) apply.

### v4.0 — Agent-native, completed (in progress)

Roadmap: `~/.claude/plans/10-v4-agent-native-completed.md`. Tickets
under `tickets/v4-*.md`. v4 wraps up the v3.x agent-native +
cost-conscious story with three themes: adoption fixed-point,
route literals as a symbol kind, one new language. **No new
architectural pillars** — see the roadmap for explicit out-of-scope
(federation, GraphStore swap, live A/B counterfactual all stay v5+).

#### Fixed

- **F1 follow-up batch (T3-T7): five smaller field-test fixes.**
  - **T3 — `search_lexical` ergonomics.** Three changes: empty results
    return `[]LexicalHit{}` not `nil` (JSON consumers see `[]` instead
    of `null`, distinguishable from "tool errored"); regex compile
    errors gain an actionable hint ("Go regexp syntax: |, ., [], (?i)
    all supported"); the path-eliminate-all error message suggests
    omitting the filter; the daemon logs a one-line stderr hint when
    `path_contains` narrowed the candidate set but no hits surfaced
    (debuggable signal in `myco daemon` output).
  - **T4 — adaptive corpus for `bench-counterfactual`.** New
    `bench.BuildAdaptiveCorpus(client)` probes the daemon
    (`list_files` → pick the heaviest indexed file by `symbol_count`
    → `get_file_outline` → pick the first non-trivial symbol) to
    construct a corpus targeting REAL symbols + files in any
    indexed repo. New `--adaptive` flag opts in. When `--repo` is
    passed without `--adaptive` and every row fails, the bench
    prints a concrete hint: "the default corpus is mycelium-tuned;
    re-run with --adaptive". Closes the F1/T4 silent-DRIFT case.
  - **T5 — counterfactual gates on success.** `Summary.OutputBytesOK`
    new field tracks bytes from successful calls only. Aggregator
    changes counterfactual basis from `OutputBytes` to
    `OutputBytesOK` so a `find_symbol` that returned `null` (4-14
    bytes for the failed envelope) doesn't earn savings credit.
    F1's session showed `-0.1% savings` because failed myco calls
    diluted the math; post-fix the model is honest — failed calls
    contribute zero to the without-myco estimate, so savings goes
    appropriately negative when myco failed and the agent paid the
    fallback anyway. Backward-compat shim: when `OutputBytesOK == 0`
    AND `OK == Count`, falls back to `OutputBytes` so v3.4-shape
    fixtures continue to work.
  - **T6 — top-level `myco find` and `myco search` aliases.** Users
    (and agents) typed `myco find symbol WorkspacePlan` and
    `myco search "WorkspacePlan|plans"` during F1 — both errored
    because the actual subcommands are buried under `myco query`.
    Two new top-level cobra commands delegate to the existing
    `runQueryFind` / `runQueryLexical` so reflexive command names
    just work. `myco find` also aliases as `find-symbol`;
    `myco search` aliases as `grep`. The original `myco query find`
    / `myco query grep` keep working unchanged.
  - **T7 — strip IDE wrapper tags from session "Task" field.** A
    session that begins with an IDE-injected `<ide_opened_file>...`
    or `<system-reminder>...` block used to surface that tag-soup
    as the session's task description in `myco session export`.
    Now `parseTranscriptReader` skips messages whose body is
    exclusively a wrapper tag (`<ide_opened_file>`, `<ide_selection>`,
    `<system-reminder>`, `<command-name>`, `<local-command-stdout>`)
    and falls through to the next user message. Mixed-content
    messages (wrapper + prose) keep their content. Nine-case unit
    test pins the heuristic.

  All five land in one bundle because each is small and they
  collectively complete the F1 v4 P0+P1+P2 follow-ups. T3 ergonomics
  + T4 adaptive corpus + T5 counterfactual honesty are the load-
  bearing fixes; T6 + T7 are usability polish.

- **Daemon fd-leak on large repos (F1/T2 EMFILE).** Codesphere
  `monorepo-4` (3079 files, 49 packages) hit `too many open files`
  on macOS during a real session because fsnotify consumes one fd
  per watched directory and the macOS default `RLIMIT_NOFILE` soft
  limit is 256. Three-layer fix:

  1. **Layer 3 (the real fix): bump `RLIMIT_NOFILE` to hard at
     daemon startup.** New `internal/daemon/RaiseFileDescriptorLimit()`
     is called in `runDaemon` before any fd-consuming code runs.
     macOS goes 256 → ~10240 (40× headroom); Linux goes 1024 →
     ~1048576 (1024× headroom). Verified live: daemon now logs
     `[daemon] RLIMIT_NOFILE raised to 1048576 (hard cap 1048576)`
     on startup. macOS quirk handled: when Setrlimit to hard fails
     (kernel `kern.maxfilesperproc` cap), we step down through
     {10240, 4096, 2048, 1024} and accept whichever the kernel
     allows. Failure is non-fatal (warning logged, daemon continues
     at original limit). Windows is a no-op (per-process handle
     limit is 16M+, not a real constraint). Build-tagged
     `rlimit_unix.go` + `rlimit_other.go` for portability.

  2. **Layer 1 (diagnostic): `daemon_fd_headroom` doctor check.**
     New `internal/doctor/fd_headroom_linux.go` reads the daemon's
     PID file (newly written by `runDaemon`) and probes
     `/proc/<pid>/fd` + `/proc/<pid>/limits` to surface fd-headroom
     pressure before EMFILE hits. WARN at 60% utilisation, FAIL at
     90% (`Thresholds.FDHeadroomWarn` / `FDHeadroomFail`). Skips
     gracefully on missing pid file (daemon not running), stale pid
     (daemon died without cleanup), or unreadable `/proc`
     (sandboxed environments). Linux-only; `fd_headroom_other.go`
     is a no-op stub on macOS / Windows because counting another
     process's fds requires `lsof` (sub-process, brittle) or
     platform-specific syscalls — and on macOS the layer 3 setrlimit
     bump preempts the EMFILE case anyway, so the check is less
     load-bearing there. Three unit tests cover the no-pid-file,
     stale-pid, and real-process paths.

  3. **Layer 2 (already shipped): Watchman backend.** Inventory
     during T2 work showed `internal/watch/watchman/` is fully
     implemented (1477 LOC, opt-in via `watcher.backend: watchman`
     in `.mycelium.yml` since v1.7). Users with monorepos beyond
     even Layer 3's headroom set the config flag and watchman takes
     over. The existing `inotify_headroom` doctor check already
     points at this path; no changes needed.

  Daemon also writes `.mycelium/daemon.pid` on startup (best-effort,
  cleaned up on shutdown via defer) so the doctor check has
  something to probe. Not exposed as a public API — implementation
  detail of T2 layer 1.

  Closes the F1/T2 blocker. Phase 2 field tests can now run on
  monorepo-scale repos without hitting the macOS 256-fd cap; Linux
  users get a doctor warning before crossing 60% utilisation.

- **TypeScript `.d.ts` indexing — the F1/T1 adoption blocker.** The
  default include glob was `src/**/*.{ts,tsx}` which narrowed TS
  coverage to files inside `src/`. Codesphere `monorepo-4`'s field
  test surfaced this: `find_symbol{name: "WorkspacePlan"}` returned
  null because the type is defined in
  `packages/payment-service/common/lib/Product.d.ts` —
  outside `src/`, never walked. Default include broadened to
  `**/*.{ts,tsx,d.ts,mts,cts}` so all common TS source locations
  (package roots, `lib/`, ambient declarations, test config files)
  are picked up; compiled outputs continue to be excluded by
  `**/dist/**` / `**/build/**`. Wizard inherits via `config.Default()`
  — no separate template change needed.

  Also fixed the `moduleName` qualifier for `.d.ts` files: previously
  `Product.d.ts` produced symbols qualified as `Product.d.WorkspacePlan`
  (the `.d` suffix wasn't stripped), now strips `.d.ts` / `.d.mts` /
  `.d.cts` as a unit so qualified names match the source file
  (`Product.d.ts` → `Product.WorkspacePlan`). Pure rename — symbol
  identity / refs continue to resolve.

  New fixture `testdata/fixtures/sample/src/types.d.ts` mirrors the
  monorepo-4 shape: `interface WorkspacePlan`, `type WorkspacePlanMap`,
  `type PlanSelector`, `enum PlanTier`, `declare module`. New
  integration test `find_symbol_in_d_ts_v4_T1_fix` asserts each
  shape returns from `find_symbol`. Existing `stats` and `list_files`
  expected file count bumped from 3 → 4 to include the new fixture.
  `task check` green; `myco bench-counterfactual` continues to pass
  (unrelated surface). Closes the F1/T1 blocker; Phase 2 Python
  field test (planned) will replicate the fixture for `.pyi` stub
  files.

#### Added

- **B3 — multi-repo bench-counterfactual harness.** The bench-counterfactual
  corpus + runner moved out of `cmd/myco/main.go` into a new
  `internal/bench/` package: `Case`, `Corpus`, `Row` types,
  `MyceliumDefaultCorpus()` returning the v3.4-calibrated mycelium-self
  corpus, `Run(client, repoRoot, corpus, language, driftThreshold)`
  the orchestrator, `PrintTable(rows, threshold, corpusName, language)`
  the renderer. The CLI command shrinks to a thin flag-parsing
  orchestrator. Two new flags: `--repo <path>` overrides the daemon
  socket location so the bench can target another mycelium-indexed
  repo without `cd`; `--language <lang>` selects the per-language
  multiplier override when one is populated. Renderer header now
  shows `corpus=<name> language=<lang>` so multi-repo runs are
  unambiguous.

  **Per-language multiplier framework wired in `counterfactualModel`.**
  `counterfactualEntry` gained a `perLang map[string]float64` field;
  new variants `EstimateCounterfactualFor(tool, bytes, language)` and
  `CounterfactualMultiplierFor(tool, language)` return the override
  when present, falling back to the default. Existing
  `EstimateCounterfactual` / `CounterfactualMultiplier` delegate with
  `language=""` for backward-compat. `ComputeSessionCostFor(...,
  language)` plumbs it through to per-row counterfactual computation;
  `ComputeSessionCost` keeps the v3.4 signature and delegates with
  empty language. **No per-language overrides populated in v4 B3** —
  the framework is wired but data hasn't been gathered. F1
  (Python/Django) and F2 (Rust/Axum) field tests will populate via
  `myco bench-counterfactual --language <lang>` and update the pinned
  test in `calibration_test.go` in the same commit.

  **Re-calibration during the v4 Phase 1 churn:** B1's preview path +
  B2's adoption.go added enough new code that referenced
  `ComputeSessionCost` to bump the `get_references` measured ratio
  from 1.65× to 1.95×. Multiplier moved 1.2 → **1.8** to match;
  `calibration_test.go` updated in the same change. The pinned
  test caught the regression on the first re-bench, exactly as
  designed.

  **Deferred to v4.1+** per the B3 ticket's honest caveats: (a) BYO
  corpus via `--corpus-file <yaml>` — useful but no consumer asking;
  (b) `--update-multipliers` source-mutating flag — flagged in the
  ticket as "If this feels too magical, drop it from v4 and require
  manual table edits — it's a convenience, not load-bearing"; (c)
  `BuildAdaptiveCorpus(client)` that probes the daemon for repo-
  appropriate targets — needs multi-repo data first to validate the
  heuristic. v4 ships the architectural extraction + per-language
  framework; v4.1+ fills in the convenience layers.

  Two new tests: `TestMyceliumDefaultCorpus_Wellformed` pins the
  corpus shape (every Case has exactly one of FallbackCmd/FallbackFile,
  every Method is a known IPC method, all nine tools covered);
  `TestCounterfactualModel_PerLanguageOverride` extends
  `calibration_test.go` to assert the per-language fallback semantics
  + pin the (currently empty) override table so accidentally-added
  overrides break the test loudly. `task check` green; live
  `myco bench-counterfactual --repo <self>` reproduces the table;
  `--repo /tmp` errors cleanly with a "start the daemon in that repo"
  message instead of a cryptic socket-not-found.

- **B2 — adoption-health doctor checks.** `myco doctor` now reads recent
  session telemetry + per-session external logs and surfaces three
  documented `docs/adoption.md` failure modes as a separate
  *adoption* section. **Findings never gate CI** — they are
  informational, do not roll into `Summary`, do not affect ExitCode.
  The classic `pass/warn/fail` Checks block is unchanged.
  Modes evaluated:
  - `search_lexical_only`: `search_lexical / total_myco_calls > 70%`
    → agent treats myco as faster grep, missing the graph layer.
  - `read_focused_under_used`: `read_focused / (read_focused + Read)
    < 15%` → agent reaches for general-purpose Read instead of the
    indexed reader (the v3.4 G2 / v4 B1 pattern made measurable).
  - `grep_over_myco`: `myco_calls / Bash/grep_calls < 1.5` → agent's
    grep reflex outpaces myco usage; CLAUDE.md priming is missing.
  Each warn ships with a `Hint` pointing the user at the docs/adoption.md
  remediation. Modes whose denominator is zero (e.g. zero file-reads
  of either kind) are silently skipped — no opinion possible, noise
  isn't useful. The fourth catalogued mode (`read_focused_without_focus`)
  is **deferred** to v4.1+ because it requires per-call params in
  the telemetry log; v4 B1's tool-side fix already gives agents
  per-call feedback so the doctor surface is less urgent.
  New CLI flags on `myco doctor`:
  - `--window <duration>` (default 7d) scopes which sessions count
    toward the evaluation. `--window 1h` for "what's been happening
    in the last hour"; `--window 720h` for monthly.
  - `--no-adoption` suppresses the adoption section entirely.
  Pure-function evaluator in `internal/doctor/adoption.go` (no DB,
  no I/O — caller hands in pre-aggregated summaries) so future
  HTTP/dashboard surfaces can reuse it. Six unit tests pin each
  failure mode at boundary values plus the rg/ripgrep aggregation
  case + the insufficient-data short-circuit. Two new telemetry
  helpers underneath: `AggregateSince(path, since)` filters the
  myco JSONL by timestamp; `SummarizeAllExternalSince(dir, since)`
  walks every `session_*_external.jsonl` and folds the windowed
  aggregate into one summary list. Both default to "no filter" when
  passed a zero `since` — backward-compat with the v3.4 callers.
  The legacy v3.4 `adoption_tool_diversity` Check is **replaced** by
  the new `search_lexical_only` finding; same signal, now in the
  separate adoption section instead of the regular Checks list.
  `task check` green; live `myco doctor` against this repo surfaces
  two real warns (read_focused_under_used at 2%, grep_over_myco at
  1.0) — the dogfooding history this very feature is meant to
  measure.

- **B1 — `read_focused` no-focus preview path.** Empty `focus` used
  to expand every symbol — Content was the entire file plus the
  outline metadata, so the call was *heavier* than a plain Read
  (the v3.4 A3 bench measured 14 KiB of myco output for a 12 KiB
  file). v4 B1 makes empty-`focus` calls return a preview instead:
  the symbol outline (via `Expanded`, unchanged shape) + the first
  `ReadFocusedPreviewLines` (default 50) lines verbatim + a new
  `Hint` field telling the agent to pass `focus=<query>` (with a
  concrete example pulled from the file's first symbol) or call
  `get_file_outline` for symbol-only listing. Files shorter than
  the cap return their full content with no Hint (no truncation
  to advertise). The non-empty-focus path is **untouched** — same
  collapse markers, same `Expanded` shape, same Stats math; only
  the no-focus branch changed. Re-bench against
  `internal/telemetry/aggregate.go` (12 KiB / 280 lines): myco
  output drops from 14 KiB → 2.8 KiB, measured ratio jumps from
  0.87× (heavier than Read) to **4.43× (lighter than Read by 4×)**.
  Counterfactual multiplier re-calibrated 1.0× → **4.0×** with
  high quality; pinned `calibration_test.go` updated in the same
  commit so the change can't be silently undone. CLI `myco read`
  prints the Hint to stderr after the content (so stdout stays
  clean for piping). MCP tool description updated to document the
  new no-focus shape so agents know what to expect.
  Three new integration tests:
  `read_focused_no_focus_returns_outline_only_envelope`
  (small file: full content + outline + no Hint),
  `read_focused_no_focus_truncates_above_cap` (cap shrunk via
  package var: Hint set, content < original, outline populated),
  `read_focused_with_focus_unchanged` (focus path snapshot).
  `task check` green; `myco bench-counterfactual` green at 11%
  drift. Closes the v3.4 A3 G2 net-negative case.

### v3.4 — Adoption fixed-point (in progress)

Gated on a non-TS field test for the route-literal + new-language
work. Today's contribution is one telemetry-darkspot doctor warning
that closes the G5 finding from the Go dogfooding pass.

#### Added

- **A3 follow-up — `myco bench-counterfactual` calibration harness.**
  New top-level subcommand that closes the calibration loop the A3
  ticket described: runs each myco tool against the live daemon, runs
  the equivalent shell fallback (`grep -rn`, `wc -c`, `find -name`),
  and compares the measured byte ratio against the modelled
  multiplier in `internal/telemetry/counterfactual.go`. Drift > 50%
  (configurable via `--drift-threshold`) on any tool exits with status
  1, except low-quality entries (graph walks the model already
  self-tags as rough) which print `info` instead of failing — a single
  corpus point shouldn't break CI on a tool that never claimed
  precision. Stale-daemon errors (`unknown method`) get a dedicated
  message pointing at the fix instead of looking like a calibration
  regression. Output formats: human-readable table (default) and
  `--format json` for machine consumption. Corpus is hard-coded
  (`ComputeSessionCost` symbol, `internal/telemetry/aggregate.go` file)
  so re-runs across machines are comparable.

  **First run surfaced four real calibration mistakes** in the v3.4
  A3 ship — the multiplier table was guessed-not-measured, and the
  bench told us where:

  | tool | pre-bench | measured | post-bench | quality |
  |---|---|---|---|---|
  | `read_focused` | 2.0× | 0.87× | **1.0×** | high |
  | `get_file_outline` | 10.0× | 2.49× | **2.5×** | medium → high |
  | `get_file_summary` | 30.0× | 2.85× | **3.0×** | medium → high |
  | `list_files` | 1.0× | 0.13× | **0.2×** | medium |
  | `find_symbol` | 0.8× | 0.84× | 0.8× (kept) | medium ✓ |
  | `search_lexical` | 1.0× | 0.72× | 1.0× (kept) | high ✓ |
  | `get_references` | 1.2× | 1.65× | 1.2× (kept) | medium ✓ |

  The `read_focused` finding is the headline G2 adoption-fixed-point
  signal made measurable: when `focus` isn't set, myco's read_focused
  output is *heavier* than a plain Read (14 KiB vs. 12 KiB on the
  bench file) because of the JSON envelope and line markers. The
  initial 2.0× guess assumed every call sets focus and harvests the
  v2.4 byte-reduction story; reality at v3.4 is closer to parity, so
  the model now stops over-crediting myco for a tool that's
  net-negative when used wrong. Block B will address the tool-side fix.

  Outline and summary were honestly wishful — a Go file with extensive
  doc comments doesn't compress to 5-15% the way the original guess
  assumed. `list_files` was the headline surprise: myco is genuinely
  *heavier* than `find` on this surface because it returns structured
  metadata (language, path, line counts) per row, while `find` emits
  bare paths. The model now reflects that honest negative-savings
  signal.

  New `internal/telemetry/calibration_test.go` pins every multiplier
  + quality tag in `counterfactualModel`. Editing the table without
  also updating the pinned test fails loudly in CI — prevents the
  silent "bumped a constant, retroactively changed every session's
  recorded savings" regression. The pinned test also catches the
  reverse case: a new tool added to the model without an entry in
  the test list. `task check` green; live `myco bench-counterfactual`
  reproduces the calibrated table on the self-index.

- **A3 — counterfactual cost model (without-myco estimate).** Answers
  the second long-term question: *"how expensive would the same session
  have been **without** myco?"*. New `internal/telemetry/counterfactual.go`
  carries a per-tool multiplier table (`find_symbol` 0.8×, `read_focused`
  2.0×, `get_file_summary` 30×, `get_neighborhood` 2.5×, `stats` 0×, etc.)
  with each entry tagged `EstimateQualityHigh|Medium|Low|None` so callers
  can downweight graph-tool guesses. `EstimateCounterfactual(tool, outputBytes)`
  is the per-call estimator; `ComputeSessionCost` now sums the estimates
  into `MycoCounterfactualBytes`, derives `WithoutMycoEstimateBytes`
  (= counterfactual + actual fallback, treated as a lower bound),
  `EstimatedSavingsBytes` (with honest negatives when myco cost more
  than the modelled alternative — the G2 adoption-fixed-point signal),
  and a `SavingsRatio` headline number in `[-1.0, 1.0]`. A
  `CounterfactualQualityMix` map counts calls per quality bucket so
  the renderer can warn when savings come mostly from low-quality
  estimates. **Modelled, not measured** — running real `grep`/`Read` in
  parallel during each myco call would 10× latency, kill the v2.4 speed
  promise, and contend with the user's editor; a v4 `--bench` mode
  could opt into the slow path. The estimate lives at the aggregate
  pass, not the write path: no schema change to `telemetry.jsonl`, so
  every existing session's savings number is recomputable. `myco session
  export` (table, markdown, json) now shows a *Modelled savings vs.
  fallback-only* trio (with-myco actual, without-myco modelled,
  estimated savings ± %), the quality-mix caveat, and a `cf bytes`
  column on the per-tool Top Contributors table. The markdown export
  surfaces an extra honesty paragraph when savings go negative
  (usually the agent reaching for myco where a single grep would have
  been cheaper). Three tests pin the math:
  `TestEstimateCounterfactual_KnownAndUnknown` (multipliers + zero
  multiplier + missing-tool fallback),
  `TestComputeSessionCost_Counterfactual` (full rollup, per-row cf
  bytes, savings sign, quality mix counts),
  `TestComputeSessionCost_NegativeSavings` (stats-only session →
  negative savings, ratio stays at 0 when WithoutMyco is 0).
  `task check` green; live `myco session export --format markdown`
  on a fallback-only session correctly shows `+0.0%` savings (no
  myco calls → no modelled saving).

- **A2 — session cost aggregator (with-myco baseline).** Answers
  *"how expensive was this session, with myco?"*. New
  `telemetry.SessionCost` + `ComputeSessionCost(myco, fallback,
  charsPerToken)` rolls per-tool byte totals (from A1) into a single
  cost block: split by source (myco vs. fallback), input vs. output,
  total bytes, and a directional token estimate via a configurable
  chars-per-token ratio. New config field
  `telemetry.chars_per_token` in `.mycelium.yml` (default 4.0, also
  exposed as `config.DefaultCharsPerToken`); non-positive values fall
  back to the default at use time. `myco session export` (markdown,
  json, table) now appends a *Cost estimate* section with the
  per-source breakdown, total bytes, estimated tokens, and a Top
  Contributors table that ranks every tool (myco + fallback) by
  byte contribution. Token numbers are explicitly documented as
  directional — for trend-watching, not billing. The "all" rollup
  row from the myco summaries is excluded from the cost calculation
  so it doesn't double-count. Three tests pin the contract:
  `TestComputeSessionCost_RollsBytesAndTokens` (full aggregation +
  ordering),
  `TestComputeSessionCost_DefaultRatio` (fallback to 4.0 on 0/negative
  inputs), `TestComputeSessionCost_EmptyInputs` (zero costs, no
  divide-by-zero). Live-tested against a fresh session: 9 Edit calls
  + 4 Bash + 6 Read = 5,960 estimated tokens, confirming the
  fallback-tool side now produces actionable numbers. Foundational
  for A3 (counterfactual savings model).

- **A1 — fallback tool output-byte telemetry.** The session-tracking
  hook recorded `input_size` for every fallback tool call (Read, Bash,
  Edit, …) but explicitly ignored `tool_response`. That left exactly
  half of the byte budget invisible — myco output was in
  `telemetry.jsonl` but Read/Bash output bytes weren't anywhere. Now
  `ExternalRecord` carries `output_size` (raw `tool_response` byte
  length), the JSONL line includes it, and `SummarizeExternal`
  aggregates per-tool `InputBytes`/`OutputBytes`. New helper
  `TotalExternalBytes(summaries)` returns the session-level total.
  `myco session export <id> --format markdown` gains an `out_bytes`
  column in the fallback-tools table and a `Fallback total bytes`
  summary line. Foundational for A2 (session cost) and A3 (modelled
  counterfactual savings). Legacy session files (without
  `output_size`) parse silently with OutputSize=0.

- **`telemetry_dark_spot` doctor check.** Closes the v3.4 G5
  field-test finding: a session-tracking-active but
  `telemetry.enabled: false` repo records the fallback half of
  agent behaviour (Bash/grep, Read, etc. via `.mycelium/session_*_external.jsonl`)
  but loses the myco-call half. `myco doctor` now warns when
  it sees `*_external.jsonl` files but `telemetry.jsonl` is
  missing or empty — exactly the state today's mycelium-on-mycelium
  dogfooding sessions left behind. Quiet on fresh repos (no
  sessions yet) and on repos with both streams populated. The
  detector is filesystem-only (no DB read, no telemetry parse) so
  it costs zero on every doctor invocation. Three new tests pin
  the three states (flag / quiet-when-both / quiet-on-fresh).

### v3.3 — Documents surface

A parallel track to the symbol graph for files whose value is in
their `(key, value)` pairs rather than callable structure. Motivated
by F3 from the v3.1 field test: agents fell to `Bash(grep -rn)` to
find i18n keys because mycelium only indexed code symbols.

#### Added

- **v3.3 documents surface (B3) — `package.json` + `go.mod` kinds + doctor coverage.**
  Closes v3.3 with two more document parsers and the doctor check.
  - `internal/parser/document/package_json.go`: matches files named
    exactly `package.json`. Emits one entry per dependency across
    `dependencies`, `devDependencies`, `peerDependencies`,
    `optionalDependencies`. Section disambiguation is not encoded in
    the entry — most agent queries are "where is this dep?", not "is
    it dev?". Token-based walk preserves per-entry line numbers.
    `workspace:*` and other non-SemVer values flow through unchanged.
  - `internal/parser/document/go_mod.go`: matches `go.mod` by
    basename. Delegates to `golang.org/x/mod/modfile` (now a direct
    dep) for parsing — block vs. single-line requires, indirect
    annotations, version validation are all handled there. Each
    require becomes a `(module path → version)` entry; indirect deps
    keep the `// indirect` marker appended to the value so agents
    can distinguish first-party from transitive without an extra
    field. `replace` and `exclude` are intentionally out of scope.
  - `Reader.Stats.DocumentsByKind map[string]int`: per-kind entry
    counts populated from a single `GROUP BY kind` query. Empty when
    no documents are indexed (additive field, `omitempty`).
  - Doctor `documents_indexed` check: emits when at least one file
    has a non-NULL `document_kind` OR `Stats.DocumentsByKind` is
    non-empty. Reports per-kind counts; warns when files registered
    with a `document_kind` produce zero entries (the symptom of a
    parser claim with no extracted rows). Code-only repos see no
    check at all — `Reader.HasDocumentFiles` is the gate.
  - Both new parsers are auto-registered alongside the i18n parser
    in `buildDocumentRegistry()` (one helper in `cmd/myco/main.go`
    shared by the indexer subcommand and the daemon's catch-up
    scan). All three kinds are always wired; they only fire when
    matching files exist, so code-only repos pay nothing.
  - Fixtures: `testdata/fixtures/documents/package.json` (deps +
    devDeps + peerDeps + workspace value), `go.mod` (direct +
    indirect requires). Eight new integration subtests cover
    per-kind indexing, exact-match-wins ordering, indirect markers,
    stats aggregation, and the doctor check across its three
    states (skipped / pass / warn).

- **v3.3 documents surface (B2) — `find_document_key` MCP tool.**
  Turns B1's stored entries into an agent-reachable surface. New
  `query.Reader.FindDocumentKey(key, kind, project, limit)` runs
  exact-then-prefix-then-substring matching against `documents.key`
  with optional `kind` (`i18n_json` | `package_json_deps` |
  `go_mod_requires`) and workspace `project` filters. Result type
  `DocumentHit` carries `path` + `project` per the v3.1.2 convention —
  pass them verbatim to `read_focused`. New IPC method
  `MethodFindDocumentKey` + `FindDocumentKeyParams`, daemon dispatch,
  MCP `mapToolToIPC` wiring (now covered by a contract test that
  every advertised tool resolves to an IPC method). Tool description
  in `pkg/mcpschema/tools.go` follows the v3.1.2 priming convention
  ("reach for this **before** `search_lexical` whenever you have a
  key like `topbar.nav.back`"). The CLAUDE.md priming snippet adds
  `find_document_key` to the navigation list; `docs/adoption.md`
  notes when the tool should appear in a healthy telemetry log
  (existing CLAUDE.md files keep their v3.1.2 snippet — the
  idempotency marker is preserved, so re-running `myco init` is a
  no-op on already-primed projects).

- **v3.3 documents surface (B1) — schema + parser infrastructure + i18n JSON kind.**
  The architectural commitment. Documents do not participate in
  `find_symbol` / `get_references` / `get_neighborhood`; agents
  reach entries via `find_document_key` (B2) and via the existing
  `search_lexical` over file content. New surfaces:
  - New migration `0007_documents.sql`: `documents` table (file_id,
    kind, key, value, line) with FTS5 trigram index over key+value,
    plus a new nullable `files.document_kind` column (NULL for code
    files, set for document-only files — keeps doctor/stats groupings
    honest without overloading `files.language`).
  - New `parser.DocumentParser` interface and `DocumentEntry` /
    `DocumentResult` types parallel to the symbol-side `Parser`.
  - New `internal/parser/document/` package with `Registry` (parallel
    to `parser.Registry`) and the first parser `I18NJSONParser`:
    handles `.json` files under any `locales/` or `i18n/` directory,
    flattens nested objects via dotted keys (`topbar.nav.back`),
    encodes array indices in the key (`items.0`), tracks the leaf
    string's source line via byte-offset binary search.
  - New `Index.UpsertDocumentFile` + `Index.ReplaceFileDocumentEntries`
    write helpers mirroring the symbol path.
  - Pipeline: new `Pipeline.Documents *document.Registry` field. When
    set, after the main symbol pass `RunOnce` runs a second walker per
    workspace with `documentWalkIncludes = {"**/*.json", "**/go.mod"}`
    (using the same excludes as the symbol walker). `Report.Documents`
    reports per-run changed document count. `HandleChange` (watcher
    path) extended to dispatch to the document registry when no
    symbol parser claims a changed file. Nil `Documents` keeps the
    legacy single-surface behaviour byte-for-byte.
  - New fixture `testdata/fixtures/documents/locales/en.json` and
    integration test `documents_integration_test.go` covering file
    registration, key flattening, line tracking, content-hash
    idempotency, and FTS-index population.

### v3.2 — Setup wizard polish & install plumbing

The setup-wizard milestone from the v3.1 plan (C1/C2/C3) plus the
install-side ergonomics that surfaced from real onboarding pain.

#### Added

- **`myco uninstall` self-command.** Mirrors `myco init` in reverse:
  prompts Y/N for each component init writes — session-tracking hook
  entries in `.claude/settings.local.json`, the post-commit git hook
  (restoring any `.mycelium-backup`), the `mycelium` entry in
  `~/.claude.json`, and the `.mycelium/` index directory — then removes
  the `myco` binaries last so the running process stays executable
  through the run. New helpers: `wizard.RemoveClaudeCodeMCP` and
  `hook.UninstallPostCommit`. Flags: `-y`/`--yes` (non-interactive),
  `--dry-run` (preview without changes), `--keep-binary` (unwire project
  state only), `--purge` (also remove `.mycelium.yml`). Binary removal
  walks `$PATH` plus the currently-running binary (and one level of
  symlink target so a release-binary install at `/opt/myco-<platform>/`
  is cleared together with its `/usr/local/bin/myco` symlink); when a
  removal needs root, the command prints the `sudo rm` invocation
  instead of escalating itself. Closes the install/uninstall asymmetry
  that left users hand-rolling `rm`s across `/opt`, `/usr/local/bin`,
  `~/.local/bin`, and `/tmp`.
- **`task install`.** Companion to `task build` that resolves
  `command -v myco`, follows one level of symlink, and overwrites the
  target so a freshly built dev binary always replaces the one `myco`
  actually runs from. Falls back to `/usr/local/bin/myco` when no
  existing install is on PATH, and uses `sudo` only when the target
  directory is not user-writable. Fixes the long-standing footgun where
  `task build` wrote to `~/.local/bin/myco` but the macOS PATH order
  silently shadowed it with whatever `/usr/local/bin/myco` already
  contained — the README's release-binary install path. CLAUDE.md and
  the README "From source" section now point at `task build && task
  install` as the canonical dev install flow.

### v3.1.2 — Workspace path fixes

The four-ticket bundle that fixed the v3.1 field test's largest
adoption defect: `read_focused` failing 7/13 times on workspace
monorepos via a path-doubling bug. v3.1.1 hotfix below only patched
the SQL match; v3.1.2 completes the disk-side fix and adds the
preventive surfaces (project annotation, description rewrites,
did-you-mean hints).

#### Added

- **Helpful "Did you mean" hints on path-not-found errors.**
  When `ReadFocused` can't resolve a path (`sql.ErrNoRows` from the
  index lookup), and when `SearchLexical`'s `path_contains` filter
  eliminates every indexed file, both methods now run a basename
  match against the `files` table and append up to 3 suggestions to
  the error message:

  ```
  file not in index: wrong/dir/server.go
  Did you mean:
    server.go  (project: api)
  ```

  Previously `SearchLexical` returned an empty result for the
  zero-candidates case, leaving the agent unable to distinguish
  "pattern not present in matching files" from "your filter
  eliminated every file" — so it would correctly conclude "no match"
  for the wrong reason. Now the headline `no indexed files match
  path_contains=…` makes the failure mode explicit. New shared
  helper `internal/query/suggest.go::suggestPaths` runs a single
  indexed query (LEFT JOIN `projects` for the project annotation)
  with `LIMIT 3`; the leading-wildcard LIKE is a full scan but on
  a 10k-file index completes in single-digit ms. No suggestions are
  emitted when basename match finds nothing — silence beats noisy
  suggestions.

- **MCP tool descriptions rewritten for workspace-mode path
  conventions.** v3.1.2. The old descriptions for `read_focused`,
  `get_file_outline`, and `get_file_summary` said "Repo-relative path
  to the file" — misleading in workspace mode where indexed paths
  are project-relative. Agents following the spec literally prepended
  the project root themselves, hitting the v3.1.2 path-doubling bug
  for the entire pre-fix lifetime. New descriptions explicitly say:
  *"Accepts the `path` returned by `find_symbol` / `list_files` /
  `get_references` verbatim, a repo-relative path, or an absolute
  path. In workspace mode the indexed form is project-relative; pass
  it through unchanged — do not prepend the project root yourself."*
  Result-returning tools (`find_symbol`, `get_references`,
  `list_files`, `get_neighborhood`, `impact_analysis`,
  `search_lexical`, `search_semantic`) now mention that the new
  `path` + `project` fields on each hit are accepted verbatim by the
  read-side tools. `path_contains` filters in `search_lexical` /
  `search_semantic` document that they match both project-relative
  and repo-relative substrings (the v3.1.2 SQL OR pattern). The
  CLAUDE.md priming snippet appended by `myco init` and the new
  *"Paths in workspace mode"* section in `docs/adoption.md` carry
  the same guidance for human-side documentation and agent priming.
- **`project` annotation on every path-bearing result type.** v3.1.2.
  `SymbolHit`, `FileHit`, `FileSummary`, `LexicalHit`, `SemanticHit`,
  `NeighborNode`, `NeighborEdge` (as `SrcProject`), `ReferenceHit` (as
  `SrcProject`), `ImpactHit`, and `PathVertex` all carry the workspace
  project name when the file belongs to a configured project, or `""`
  when it doesn't. Each query joins `LEFT JOIN projects p ON p.id =
  f.project_id` and selects `COALESCE(p.name, '')`. JSON tags use
  `omitempty` so single-project mode emits no visible change. Motivation:
  agents in a multi-project workspace previously got back paths like
  `src/index.ts` from `find_symbol` with no way to know which of the
  five packages it belonged to — leading to guessed prepended prefixes
  (the v3.1.2 path-doubling bug). With `project` annotated, agents can
  pass `path` and `project` back verbatim to `read_focused` (or just use
  the existing `project` filter on read queries with confidence).

#### Fixed

- **Workspace-mode path-doubling.** A v3.1.1 follow-up.
  `ReadFocused`'s SQL match accepted both project-relative and
  repo-relative input paths, but the on-disk path reconstruction still
  used the **input** path in `filepath.Join(repoRoot, projectRoot, path)` —
  so a repo-relative input (e.g. `packages/ui-tests/src/foo.ts`) got the
  project prefix prepended a second time and produced
  `…/packages/ui-tests/packages/ui-tests/src/foo.ts: no such file or
  directory`. A field-test session (`monorepo-4`) had `read_focused`
  failing 7/13 times via this path. Fix: also `SELECT f.path` from the
  LEFT JOIN and use the database value for the disk-side join. Absolute
  inputs are now normalised to repo-relative up front before the SQL
  match, so `read_focused` accepts project-relative, repo-relative, and
  absolute paths uniformly. `SearchLexical`'s `path_contains` filter now
  matches both path forms via the same OR pattern; previously a filter
  like `path_contains: "services/api"` against a workspace project
  returned zero candidates because the LIKE was applied only to
  project-relative `f.path`. Worker-side `os.Open` ENOENT no longer
  silently `continue`s — it logs to daemon stderr so stale-index or
  path-reconstruction bugs fail loudly in regression runs. New tests in
  `workspace_integration_test.go` cover all three input forms for
  `ReadFocused`, both forms for `SearchLexical` `path_contains`, and
  pin the (already-correct) behaviour of `GetFileSummary` /
  `GetFileOutline` against future refactors.
### v3.1.1 — Workspace-mode disk-read hotfix

Partial fix superseded by v3.1.2. Documented here so the chronology
of how the bug got resolved survives.

#### Fixed

- **Workspace-mode disk reads.** `ReadFocused` and
  `SearchLexical` both joined `repoRoot` with the index-stored path
  to locate the file on disk, but in workspace mode (v1.5+) the
  index stores **project-relative** paths — the disk file lives at
  `repoRoot + projectRoot + path`. Pre-fix consequences observed in
  a v3.1 field test:
  - `read_focused` failed unconditionally on any monorepo workspace
    project, returning `"no such file or directory"` for the
    canonical path that `list_files` had just returned.
  - `search_lexical` silently swallowed the read error and returned
    zero results (the worker `continue`s on read failure), so a
    lexical search across an entire monorepo project would look like
    a real "no matches" — false negatives that destroyed agent trust.
  Both tools now `LEFT JOIN projects ON projects.id = files.project_id`
  to recover the project root and prepend it to the disk-side path.
  Single-project mode (`project_id` NULL) keeps the existing
  `repoRoot + path` join. New regression tests in
  `workspace_integration_test.go` cover both code paths against the
  existing 3-project fixture. (Disk-side path construction still
  used the input path here, not the database value — fully resolved
  in v3.1.2 above.)

### v3.1 — Adoption-driven fixes

First slice of the broader-hyphae roadmap. Three surgical changes
targeting the field-test findings from a real TS-monorepo session:
agents fell into the documented "search_lexical only" pattern,
`find_symbol` returned `null` instead of an empty list with a hint,
and `read_focused` was never reached for despite multiple full-file
reads.

#### Added

- **v3.1 adoption-driven fixes (A1 + A3 + A4).** Three surgical changes targeting the field-test
  findings from a real TS-monorepo session: agents fell into the
  documented "search_lexical only" pattern, `find_symbol` returned
  `null` instead of an empty list with a hint, and `read_focused`
  was never reached for despite multiple full-file reads.
  - **A4 — `Stats.ConfiguredProjects` + `projects_configured_but_empty`
    doctor check.** New `ProjectStats` shape (`name`, `root`,
    `file_count`) populated from a LEFT JOIN on `projects` × `files`,
    so a configured project whose include glob matched nothing still
    appears with `file_count=0`. Doctor fails when any configured
    project has zero files (likely a misconfigured `include` or
    `root` in `.mycelium.yml`), warns under 10 files (likely a
    too-narrow include), passes otherwise. Skipped entirely when no
    `projects:` block is configured (single-project mode, the
    default — keeps the report clean for the common case). New
    thresholds `EmptyProjectFail` / `EmptyProjectWarn` in
    `doctor.Thresholds`. Surfaced via `myco stats` as a per-project
    file-count line. Tests in
    `internal/doctor/doctor_projects_test.go` cover skip /
    pass / warn / fail paths.
  - **A1 — `FindSymbolResult{Matches, Hints}` envelope.**
    `Reader.FindSymbol` now returns a result struct instead of a
    bare `[]SymbolHit`. `Matches` is always non-nil (empty slice,
    not nil → JSON `[]`, not `null`). When `Matches` is empty,
    `Hints` populates with diagnostic lines explaining why a filter
    eliminated everything: typo'd project name (with the configured
    project list), `kind` filter that eliminated all real matches
    (with the kinds the name actually matches), or unknown kind
    value (with the index's known kinds). New `internal/query/diagnose.go`
    holds the helpers; they only run on the empty-result path so
    the hot path stays unchanged. Hint phrasing is intentionally a
    flat `[]string` of human-readable lines so wording can iterate
    without breaking schema. Integration tests in `integration_test.go`
    cover successful match (no hints), bogus project (project hint),
    kind-eliminated-all (kind hint), and genuine miss (no hints).
  - **A3 — MCP tool descriptions rewritten for first-reach priming.**
    Every entry in `pkg/mcpschema/tools.go` follows a uniform "what
    it does + when to reach for me" shape. The five most-affected
    tools (`find_symbol`, `read_focused`, `get_references`,
    `get_neighborhood`, `search_lexical`) explicitly contrast with
    the wrong-tool reflex agents fell into during the field test —
    e.g. `search_lexical` now reads "Use this **only** for literal
    strings or regex patterns. For symbol navigation prefer
    `find_symbol`; for 'who calls X' prefer `get_references`."
    Wording stays competitor-neutral ("the agent's general-purpose
    file reader") rather than naming Claude Code tools literally
    so it survives client renames. Structural test in
    `pkg/mcpschema/tools_test.go` locks in ≥ 2 sentences + a
    reach-for-me cue (one of `reach`/`use`/`instead`/`prefer`/
    `before`/`after`) for every tool, plus a stricter contrast
    assertion for the five high-priority tools.

#### Changed

- **`Reader.FindSymbol` return shape.** `[]SymbolHit, error` →
  `FindSymbolResult, error`. Direct callers in this repo
  (`cmd/myco/main.go`, integration tests) updated; external
  consumers of the IPC / HTTP / MCP `find_symbol` method see a JSON
  shape change from `[…]` to `{"matches":[…], "hints":[…]}`. Other
  query methods (`get_references`, `search_lexical`, etc.) keep
  their bare-list shape — extending the envelope to them is a v3.2
  / v3.3 decision once the shape proves itself in the field-test
  re-run.

### v3.0 — Agent-native release & v2.5 incremental skills

Documentation pass, release-binary packaging, and the v2.5 hash-gated
skills regen. Originally separate releases but bundled here because
the merge of "agent-native polish" and "skills become cheap to
regenerate" was the v3.0 story in practice.

#### Changed

- **v3.0-rc polish + docs.** Canonicalises the `docs/` layout (the
  old root `RESEARCH.md` moves to `docs/research.md` and gains a
  design-decision crosswalk plus a "read but not acted on" section),
  rewrites the README around the v3 agent-native story (skills tree
  and focused reads as the headline; structural MCP tools demoted
  to "for programmatic use") while leaving the project header /
  badges untouched, ships `docs/adoption.md` as a guide to verifying
  agent uptake via the v2.2 telemetry log, and adds a navigation
  integration test (`navigation_integration_test.go`) that
  mechanises `docs/navigation-example.md` so the
  `INDEX.md → SKILL.md → read_focused` path is enforced in CI.
  Release tarballs now bundle the matching sqlite-vec shared library
  next to the binary, and `index.OpenWithExtension` auto-discovers
  it when `index.vector.extension_path` is left empty in
  `.mycelium.yml` — semantic search at scale is now zero-config on
  release builds.
- **Incremental skills regeneration (Pillar H, v2.5 in the v3 plan).**
  The v2.3 skills tree gets a hash gate: every rendered file (per-
  package SKILL.md, per-aspect INDEX.md, root INDEX.md) is hashed
  before write; if `skill_files.skill_hash` matches, the WriteFile and
  store update are both skipped. New migration `0006_skills.sql`
  introduces the `skill_files` table; new `internal/index` helpers
  (`SkillFileHash`, `UpsertSkillFile`, `DeleteSkillFile`,
  `PruneSkillFiles`, `ListSkillFiles`) satisfy a small `skills.Store`
  interface so the renderer stays storage-agnostic. `Compile` grows
  `Options.Store`, `Options.Stats`, `Options.DryRun`; passing a Store
  enables hash-gated writes, and Stats reports `Rendered / Written /
  Skipped / Pruned`. The wall-clock `generated:` frontmatter line is
  stripped from the hash input so two renders of the same structural
  content produce the same hash regardless of when they ran — without
  this the gate would fire on every daemon batch and defeat the
  whole milestone.
- **Daemon-driven incremental regen.** New `Daemon.SkillsRegen
  func(ctx, packages []string) error` field plus a debounced batcher in
  the watcher event loop: every `path.Dir(relPath)` from
  `Pipeline.HandleChange` is collected into a dedup set, and after
  `SkillsDebounce` (default 200ms) of channel idle the batch is
  flushed to a worker goroutine that calls SkillsRegen exactly once.
  A second worker serialises regen calls so two bursts can't race on
  the same `.mycelium/skills/` tree. `cmd/myco daemon` wires
  SkillsRegen to `skills.RegenerateAffected` only when
  `.mycelium/skills/` already exists, so users who never opted into
  the skills feature aren't surprised by a regenerated tree.
  `RegenerateAffected` for v2.5 is a thin wrapper over `Compile` with
  the Store set: the per-render cost is ~100ms on the self-index and
  the hash gate makes the actual write cost zero on a clean tree, so
  fully exploiting per-package short-circuiting was deferred — the
  packages slice is captured for telemetry and reserved for a future
  optimisation hook.
- **`skills_coverage` doctor metric.** New `Stats.SkillsPackagesIndexed`
  (distinct directories holding indexed files) plus a filesystem walk
  in `internal/doctor` that counts present `SKILL.md` files under
  `.mycelium/skills/packages/`. Coverage = on-disk / indexed; pass at
  ≥ 0.95, warn below, fail below 0.5. Skipped when the skills dir
  doesn't exist (opt-in feature, not a regression). Walking the
  filesystem rather than reading `skill_files` catches the case where
  the DB row outlives the file on disk.
- **`myco skills compile --status` and `--incremental` flags.**
  `--status` runs the renderer in DryRun mode against the live
  `skill_files` hashes and reports `rendered / unchanged / would
  change` without touching disk or the DB. `--incremental` is the
  hash-gated equivalent of `compile`: it writes only the files whose
  rendered bytes differ and prints the same per-call counters the
  daemon logs.

#### Measured

- **v2.5 hash gate on the self-index (Tiger Lake, 105 files / 30
  packages / 35 rendered files).** Cold compile: 35 rendered, 35
  written, ~100ms. Warm compile (no source changes): 35 rendered, 0
  written, 35 skipped, ~70ms. Single-symbol change (added one
  top-level `func`): 35 rendered, 6 written, 29 skipped — the changed
  package + root INDEX.md + four aspect indices. Pure-formatting
  source change (added a blank line to a comment): 35 rendered, 0
  written, 35 skipped — the index hash didn't move, so neither did
  the SKILL.md hash.

### v2.4 — Focused reads

The token-saving read primitive. Lexical filter, no neural model —
keeps the single-binary distribution story while picking up the
"hide irrelevant symbols" pattern from SWE-Pruner.

#### Added

- **Focused reads (Pillar I).** New
  `internal/focus` package implements the deterministic lexical filter
  promised by the v3 roadmap: tokenize a focus string (lowercase,
  stopword-strip), then score candidates against name (3.0 exact / 2.0
  substring), qualified name (2.0 substring), docstring (1.0
  substring), and ref targets (0.5 substring). Pure Go, no neural
  model — we adopt the SWE-Pruner *pattern* but explicitly not the
  *mechanism*, so the single-static-binary distribution story holds.
  Wired into three existing reader methods as an optional `focus`
  param: `FindSymbol` drops non-matchers and re-ranks survivors by
  score; `GetFileOutline` keeps top-level items whose subtree
  contains any match; `GetNeighborhood` prunes nodes outside the
  focus and surfaces a `focus filter pruned N node(s)` note. Empty
  focus is byte-identical to prior behaviour — verified by the
  pre-existing integration suite.
- **`read_focused` MCP tool / `myco read` CLI.** New top-level read
  primitive that returns one indexed file with non-focus-matching
  symbols collapsed to one-line markers in the file's native
  comment style (`// signature ...  // collapsed (lines N-M)` for
  Go/TS/JS, `# ...` for Python). Empty focus returns the file in
  full, so the tool also functions as a daemon-mediated `cat` when
  the agent isn't sure how big the file is. Multi-line signatures
  (Go interface bodies, struct definitions) are flattened to their
  first line with `…` appended so the marker stays single-line.
  Response carries a `Stats { TotalSymbols, ExpandedSymbols,
  OriginalBytes, ReturnedBytes }` block plus an `Expanded` list of
  surviving symbols with their original `[StartLine, EndLine]` ranges
  so agents can map back to source. Wire-up: new `Focus` field on
  `FindSymbolParams`/`GetFileOutlineParams`/`GetNeighborhoodParams`,
  new `ReadFocusedParams` + `MethodReadFocused`, daemon dispatch,
  MCP tool schema entry, HTTP route auto-derived from the
  dispatcher, `--focus` flag on `myco query find|outline|neighbors`,
  and `myco read <path> --focus "<q>"` (with `--stats` for the
  collapse counters on stderr).

#### Measured

- **`read_focused` byte reduction (self-index, Tiger Lake).** Three
  representative queries on this repo:
  | file | focus | returned/original | reduction |
  |---|---|---|---|
  | cmd/myco/main.go (44 KB) | "telemetry recorder" | 8443 / 44337 | 81% |
  | cmd/myco/main.go (44 KB) | "skills compile" | 8909 / 44337 | 80% |
  | internal/daemon/daemon.go (9 KB) | "dispatch read_focused" | 6540 / 9163 | 29% |
  Results vary with focus specificity and file shape — large files
  with many independent symbols collapse aggressively, small dense
  files less so. We're explicitly not claiming SWE-Pruner's 23–54%
  range against a trained reranker; the lexical filter trades
  precision for distribution simplicity.

### v2.3 — Static skills tree

The agent-readable static index. Markdown-only, generated from the
graph, navigable with just `Read`.

#### Added

- **Static skills tree (Pillar L).** New
  `internal/skills` package + `myco skills compile` CLI generate a
  deterministic Markdown tree under `.mycelium/skills/` that an agent
  can navigate with only the `Read` tool. Layout: per-package
  `SKILL.md` (one per directory of source, language unified for
  mixed-language directories), root `INDEX.md` listing every package,
  and an `aspects/` subtree with four cross-cutting filters
  (error-handling, context-propagation — clean signature matches;
  config-loading, logging — heuristic ref-driven, frontmatter-flagged).
  Output is `language: complementary` to MCP — SKILL.md is lean
  (≤~160 lines on the largest mycelium package), points the reader at
  `myco query refs/neighbors` for specifics. New reader helpers
  `(*query.Reader).PackageRefAggregates`,
  `SymbolsBySignatureLike`, `SymbolsByOutboundRef` keep the "query is
  the only reader" rule intact. `--package` and `--aspect` flags
  scope regen for fast iteration; both correctly skip everything
  outside their scope. Self-dogfood on the mycelium repo: 28 packages
  / 88 files / 589 symbols, full tree compiles in ~52ms; tree
  gitignored as a sibling of `index.db`. Incremental hash-gated
  regeneration is v2.5.
### v2.2 — Opt-in telemetry log

#### Added

- **Opt-in telemetry log (Pillar K).** New
  `internal/telemetry` package with a `Recorder` interface and a
  JSONL `FileRecorder`. Off by default; enabled via
  `telemetry: { enabled: true }` in `.mycelium.yml`. When on, the
  daemon dispatcher in `internal/daemon/daemon.go` records one line
  per IPC/MCP call to `.mycelium/telemetry.jsonl` (timestamp, tool
  name, input bytes, output bytes, wall-clock ms, ok). No network,
  no aggregation off-host — purely a local file the user can
  `tail -f`. Open failure falls back to `Disabled` so observability
  never gates daemon startup.
- **`myco stats --telemetry`** aggregator: streams the JSONL log and
  prints per-tool counts, byte totals, and p50/p95 durations, plus
  an `all` rollup. Friendly hints when telemetry is off in config or
  when no records exist yet, so users who flipped the flag but
  haven't generated traffic understand what they're seeing.

### v2.0.x — Post-rc1 fixes & benchmarks

#### Fixed

- **sqlite-vec extension entrypoint.** `LoadExtension` was being called
  with an empty entry symbol, which makes SQLite derive the symbol name
  from the filename (`vec0.so` → `sqlite3_vec0_init`). The shipped
  library exports `sqlite3_vec_init` regardless of filename, so loading
  failed with an empty `undefined symbol:` error. Now pass the explicit
  entry in `internal/index/vss.go`.

#### Measured

- **Semantic search benchmark matrix** — ran the full grid (10k /
  50k / 100k chunks × 384 / 768 / 1536 dims × {brute-force, vec0})
  on Tiger Lake. vec0 is a consistent 5-8× speedup over pure-Go
  brute-force; absolute numbers land in README. Important finding:
  at sqlite-vec v0.1.9 the vec0 path is SIMD-optimized *flat* scan,
  not HNSW, so both paths scale linearly in the corpus. The
  roadmap's "p95 < 50ms at 100k chunks" target is not met on
  laptop-class CPU — vec0 at 100k/768 is 171 ms. The 50 ms
  threshold holds up to ~50k/384 with vec0. Benchmark is
  reproducible via `MYCELIUM_VEC_PATH=... go test -bench=...`.

## [v2.0.0-rc1] — 2026-04-24

First release candidate for v2.0 ("precision and scale"). No new
functional changes since v1.7; this tag consolidates the v1.1 → v1.7
series into a single release and gates the remaining v2.0 work.
Per-milestone details remain in the sections below.

### Delivered against the v2.0 acceptance criteria

- **Type-aware references** for Go, TypeScript, Python. Self-index
  reports `self_loop_count = 0`, `unresolved_ref_ratio = 0.0%`.
  (v1.2, v1.3)
- **Workspace mode**: one daemon, one SQLite, N sub-projects with
  per-project config and optional `project` filter on every query
  tool. (v1.5)
- **Graph-native tools**: `impact_analysis`, `critical_path`. (v1.6)
- **PR-scoped queries**: `--since <ref>` on five read methods. (v1.6)
- **Doctor + quality signals**: `myco doctor` exits 0/1/2 on
  pass/warn/fail with configurable thresholds. (v1.1, v1.2, v1.7)
- **Watchman opt-in** behind `watcher.backend`. (v1.7)
- **sqlite-vec integration** compiled in; brute-force fallback
  measured. (v1.4)

Architectural invariants from v1.0 are preserved: SQLite is still
source of truth and query engine; `internal/query` is the sole
reader; `internal/pipeline` is the sole writer; no new top-level
processes; all schema changes are additive.

### Known gaps before the final v2.0 tag

- **`libsqlite_vec.{so,dylib,dll}` not bundled** in the release
  tarball. Users install `sqlite-vec` manually per the README.
- **No 100k+ file monorepo validation.** `myco doctor`,
  workspace mode, and the inotify-headroom check have only been
  exercised against the self-index and the committed fixtures.
- **Roadmap p95 target not met.** The "p95 < 50ms at 100k chunks"
  metric from the v2.0 plan was aspirational against an HNSW-style
  index; sqlite-vec v0.1.9 is flat SIMD scan so neither path hits
  50 ms at 100k/768 on laptop-class CPU. Full matrix in the
  benchmark table (see README). HNSW in sqlite-vec upstream is the
  path forward; not gating v2.0 final.

## [v1.7.0] — 2026-04-24

"Watchman opt-in" — the seventh v2.0 milestone (Pillar G). Pluggable
watcher backend so users on 100k+ file repos can escape the
`fs.inotify.max_user_watches` ceiling without changing anything
else about how mycelium runs.

### Added

- **`internal/watch/watchman/`** — minimal in-tree watchman client.
  Talks JSON-over-unix-socket: `get-sockname`, `watch-project`,
  `subscribe`, `unsubscribe`. Read pump demultiplexes command
  responses vs subscription deliveries so one connection handles
  both. `$MYCELIUM_WATCHMAN_SOCK` overrides sockname discovery for
  container setups.
- **Watcher backend selection.** New `watcher.backend` config field
  (`"fsnotify"` default, `"watchman"` opt-in) plus
  `myco daemon --watcher-backend <name>` CLI override. Unknown
  values are a hard error; watchman unavailability falls back to
  fsnotify with a stderr warning so the daemon still starts.
- **`internal/watch` restructure.** Old monolithic `watch.go` split
  into `watcher.go` (public `Watcher` interface + `Options`),
  `common.go` (shared debounce/coalesce/filter wrapper), and
  per-backend sources: `fsnotify.go`, `watchman.go`. Both backends
  route through the same wrapper so behavior is identical — the
  two honest-surface bugs the old struct had (unused
  `MaxFileSizeKB`, unused `CoalesceMS`) are fixed once, not twice.
- **`CoalesceMS`** is now wired. Bursts of debounced events within
  a coalesce window flush as one batch to the output channel.
- **Doctor: `inotify_headroom` check.** Linux-only. Counts repo
  directories vs `/proc/sys/fs/inotify/max_user_watches` and warns
  above 50%, fails above 90%. The warn message suggests either
  switching to `watcher.backend: watchman` or raising the sysctl.

### Changed

- `watch.New` signature went from positional args to an `Options`
  struct (source-incompatible; migrates cleanly — all call-sites
  updated).
- `daemon.Daemon.Watcher` is now `watch.Watcher` (interface) rather
  than `*watch.Watcher` (struct pointer), matching the new backend
  split.

### Fixed

- Shutdown race in the watcher's shared wrapper: coalesce/debounce
  timers could fire `w.send` after the output channel closed. Pump
  now owns every write to `out`; timers signal through internal
  channels. `go test -race ./internal/watch/...` confirms.

## [v1.6.0] — 2026-04-24

"Graph-native tools + PR scope" — the sixth v2.0 milestone (Pillars E
+ F). Two new graph traversals that become cheap once v1.2/v1.3's
type-aware resolvers landed, plus a `--since <ref>` path filter on the
existing read surface for PR-scoped queries.

### Added

- **`impact_analysis(symbol)`** — new MCP tool and CLI `myco query
  impact`. Returns the transitive inbound closure around a symbol as
  a flat list ranked by distance (1 = direct caller). Optional `kind`
  filter narrows the reported set (typical use: `kind=method` to find
  test methods covering the target). Default depth 5, hard ceiling
  10. Composes with `project` and `since` — they scope the *reported*
  callers, not the walk, so cross-file / cross-project chains still
  surface.
- **`critical_path(from, to)`** — new MCP tool and CLI `myco query
  path`. Returns up to `k` shortest outbound call paths. Bounded BFS
  at depth ≤ 8 via a single recursive CTE; cycles prevented by the
  SQLite `instr()` idiom on a comma-delimited accumulated path
  column. Hydrates the distinct vertices in one second-pass query to
  avoid the N+1 fan-out. Default k = 5.
- **`--since <ref>` filter** on `find_symbol`, `get_references`,
  `list_files`, `search_lexical`, `search_semantic`. Resolved via
  `git -C <root> diff --name-only <ref>...HEAD` at the transport
  boundary (daemon RPC handler and CLI offline fallback), then passed
  to the reader as `pathsIn []string`. Three-dot form uses the merge-
  base so "files on my branch" stays correct after the base advances.
- **`internal/gitref/`** — thin helper (`ResolveSince`) that runs the
  `git diff` with a 5s timeout and surfaces stderr verbatim on
  failure. Returns a non-nil empty slice when the ref has no diff
  against HEAD so the reader's zero-row sentinel distinguishes "no
  changes" from "no filter."
- **`internal/query/graph.go`** — `ImpactAnalysis`, `CriticalPath`,
  `ImpactHit`, `Impact`, `PathVertex`, `CriticalPathResult`. Reuses
  `resolveSeed` and `loadNode` from `neighborhood.go`.
- **`internal/query/paths.go`** — shared `pathsInClause` splicer
  renders the `AND f.path IN (?, ?, ...)` WHERE fragment used across
  the five filtered methods. Caps the path list at **500 entries**
  (SQLite's 999-parameter limit) and returns a clear error when a PR
  diff expands beyond that — the correct fix is a tighter base ref.
- **Reader signature change** (additive, source-incompatible) — five
  methods gained a final `pathsIn []string` argument:
  - `FindSymbol(ctx, name, kind, project, limit, pathsIn)`
  - `GetReferences(ctx, target, project, limit, pathsIn)`
  - `ListFiles(ctx, language, nameContains, project, limit, pathsIn)`
  - `SearchLexical(ctx, pattern, pathContains, project, k, repoRoot, pathsIn)`
  - `Searcher.SearchSemantic(ctx, query, k, kind, pathContains, project, pathsIn)`

  `pathsIn = nil` is "unscoped"; `pathsIn = []string{}` is an
  explicit zero-row sentinel. Existing callers pass `nil` to preserve
  prior behavior. An options-struct refactor was considered and
  rejected for mid-release API churn.
- **MCP tool schemas** — two new tool entries (`impact_analysis`,
  `critical_path`), plus a `since` input on `find_symbol`,
  `get_references`, `list_files`, `search_lexical`,
  `search_semantic`. MCP server dispatch in `internal/mcp/server.go`
  routes the two new tools.
- **CLI subcommands** — `myco query impact <symbol>` and `myco query
  path <from> <to>`. `--since <ref>` added to `find`, `refs`, `files`,
  `grep`, `search`. Offline fallback path runs `gitref.ResolveSince`
  locally so `--since` works even without the daemon.
- **Integration tests** at `graph_integration_test.go`:
  - `TestIntegration_ImpactAnalysis` — seeds on `auth.normalizeEmail`
    and asserts `auth.AuthService.fingerprint` at distance 1 and
    `auth.AuthService.issueToken` at distance 2. Subtests for the
    kind-filter narrowing and the depth-clamp note.
  - `TestIntegration_CriticalPath` — asserts the path `issueToken →
    fingerprint → normalizeEmail` surfaces.
  - `TestIntegration_PathsInFilter` — exercises the reader-level
    filter (no git process) across three cases: matching file,
    non-matching file, empty-slice sentinel.
- **`internal/gitref/resolve_test.go`** — temp-git-repo tests covering
  the happy path (two-commit diff), empty ref (error), unknown ref
  (error), and the no-changes case (non-nil empty slice).

### Notes

- `vec0` KNN fast path is skipped when `search_semantic` is called
  with a `project` filter (v1.5) **or** a `since` filter (v1.6) —
  `vec0 MATCH` doesn't compose with arbitrary `WHERE` clauses.
  Brute-force cosine handles scoped semantic search.
- `impact_analysis` is intentionally not a superset of
  `get_neighborhood(direction=in)`. The shapes serve different
  workflows: graph (nodes + edges) vs. flat distance-ranked list; 2
  vs. 5 default depth; 5 vs. 10 max; no kind filter vs. yes.
- Cross-repo federation (N worktrees, one graph) remains a v3
  non-goal.

### Verification

Integration suite green on `TestIntegration_IndexAndQuery`,
`TestIntegration_WorkspaceMode`, `TestIntegration_ImpactAnalysis`,
`TestIntegration_CriticalPath`, `TestIntegration_PathsInFilter`, plus
all four `internal/gitref` cases. `go vet -tags sqlite_fts5 ./...`
clean. No schema changes, no migration.

## [v1.5.0] — 2026-04-23

"Workspace mode" — the fifth v2.0 milestone (Pillar C). One daemon, one
SQLite, N sub-projects under one worktree. Not cross-repo federation
(that's v3): the unit of isolation is a directory inside the same repo,
each with its own `languages` / `include` / `exclude` overrides.

### Added

- **Migration `0005_projects.sql`** — new `projects(id, name, root,
  created_at)` table plus `files.project_id` FK with cascade delete. A
  NULL `project_id` means the file belongs to the implicit root project
  (v1.4 configs keep working untouched).
- **`config.ProjectConfig`** — optional `projects:` list in
  `.mycelium.yml`. Each entry has `name`, `root`, and optional
  `languages`/`include`/`exclude` overrides. Embedder/chunking stay
  inherited from the top level (one DB can't mix embedding dims).
- **`internal/index/projects.go`** — `UpsertProject`, `PruneProjects`,
  `ListProjects`. Idempotent upsert by name; prune drops rows no longer
  in config (cascades remove their files + symbols + refs + chunks).
- **`pipeline.Workspace`** — per-project walker + project_id. The
  pipeline now accepts a `Workspaces []Workspace` slice; each walker
  runs with its own roots/filters and every file it emits is tagged
  with the owning project before hitting the writer. Legacy single-
  `Walker` mode still works when `Workspaces` is empty.
- **`Pipeline.FileProjectFor`** — longest-prefix resolver so fsnotify
  events from the watcher can attribute a changed file back to its
  project on the single-file update path.
- **Query-side `project` parameter** — `FindSymbol`, `GetReferences`,
  `ListFiles`, `SearchLexical`, `SearchSemantic`, `GetNeighborhood`
  each accept an optional project name. A splicer (`projectScope`) adds
  `AND f.project_id = ?` when set; unknown project names return zero
  hits rather than silently falling back to unscoped (config bug
  visibility). For `GetNeighborhood`, only the seed lookup is scoped —
  traversal stays global so cross-project call graphs surface.
- **IPC + MCP + CLI plumbing** — `Project` field added to every
  params struct that touches files. MCP tool schemas advertise the
  optional `project` input. CLI gains `--project <name>` on `myco query
  find | refs | files | grep | search | neighbors`.
- **Workspace integration test + fixture** at
  `testdata/fixtures/workspace` (3 sub-projects: Go `api`, TS `web`,
  Python `worker`) in `workspace_integration_test.go`. Verifies
  per-project scoping on `find_symbol` and `list_files`, the
  unknown-project zero-hit contract, and that every indexed file has a
  non-null `project_id` pointing at the right row.

### Notes

- The vec0 fast path is skipped when a project filter is active — vec0
  MATCH doesn't compose with arbitrary WHERE clauses. Brute-force
  cosine handles project-scoped semantic search.
- Embedder inheritance is intentional: a single SQLite DB can't mix
  embedding dimensions cleanly, so per-project embedder overrides are
  deliberately out of scope.

## [v1.4.0] — 2026-04-22

"Semantic at scale" — the fourth v2.0 milestone (Pillar B). Adds optional
[sqlite-vec](https://github.com/asg017/sqlite-vec) integration behind
runtime feature detection. Brute-force Go cosine stays as the honest
fallback; nothing breaks when the extension is missing.

### Added

- **`internal/index/vss.go`** — extension loader via a per-process named
  driver + `ConnectHook` that auto-loads the library on every new DB
  connection. `EnsureVSS(dim)` creates a `vss_chunks` virtual table
  named by dimension and backfills rows from any pre-existing
  `chunks.embedding`. `VSSAvailable()` and `VSSTableName()` let callers
  branch at query time.
- **`index.OpenWithExtension(path, extPath)`** — new opener that
  transparently handles both the extension-loaded and fallback cases.
  `index.Open(path)` keeps its pre-v1.4 behavior.
- **Dual-write in `WriteEmbedding`** — every embedding lands in both
  `chunks.embedding` (source of truth / fallback) and `vss_chunks`
  (KNN index). Mirrored in one transaction; safe to lose either.
- **`Searcher.VSSTable`** — opt-in fast path. When set and the user has
  no kind/path filter, `SearchSemantic` issues `embedding MATCH ? AND
  k = ?` against vec0 and skips the scan. Falls back softly on any
  query error (e.g. table missing for a changed dim).
- **Config** — `index.vector.extension_path`, `index.vector.auto_create`,
  `index.vector.ef_search` (reserved for HNSW tuning when vec0 ships it).
- **`embed.UnpackInto`** — alloc-free variant of `Unpack` used in the
  brute-force hot loop. Avoids 100k `[]float32` allocations per query
  at 100k-chunk scale.
- **Two-pass brute-force search** — first pass scans only `(id,
  embedding)` columns to find top-k; second pass hydrates the 10
  winners with path/symbol/content. Eliminates ~30× the per-row I/O
  vs v1.3. At 10k chunks this took latency from 166 ms → 114 ms.
- **Semantic-search benchmark matrix** at
  `internal/query/semantic_bench_test.go` — 10k / 50k / 100k / 768 dim
  on brute-force. Numbers published in README.

### Measured

On an Intel i7-1165G7 (Tiger Lake), 768-dim, brute-force fallback, k=10:

| corpus | p50 |
|---|---|
| 10k chunks | ~114 ms |
| 50k chunks | ~555 ms |
| 100k chunks | ~1.10 s |

The plan's aspirational target was <50 ms at 100k via vec0 KNN. That
requires the extension installed; the brute-force path is ~22× slower
at 100k but still correct. The vec0 fast path is architecturally
complete but *untested in this release* — validate on your machine with
the install recipe in README.

### Honest scope note

The vec0 KNN code path in `Searcher.searchViaVSS` is written and
compiles, and the dual-write + extension-loading plumbing is tested on
the fallback path (no extension present in this dev env). We do not
claim measured vec0 numbers until a contributor benchmarks with the
extension loaded.

## [v1.3.0] — 2026-04-22

"TS and Python scope resolvers" — the third v2.0 milestone (Pillar A,
completed for non-Go languages). Brings v0 textual refs up to the
visited-and-stamped floor for TypeScript (`ResolverVersion=2`) and
Python (`ResolverVersion=3`).

### Added

- **`internal/resolver/python`** — stateless per-file resolver. Handles
  `import` / `from-import` bindings (including aliases), `self.method()`
  and `cls.method()` inside classes, module-qualified calls like
  `foo.bar()` via namespace-style imports. Every visited call is stamped
  `ResolverVersion=3` so the SQL short-name fallback skips it.
- **`internal/resolver/typescript`** — same shape for TS/TSX. Named
  imports + aliased imports + default imports + `import * as ns`
  namespace imports all resolve. `this.method()` inside classes resolves
  to the class's own methods. Stamps `ResolverVersion=2`.
- **`pipeline.Resolver` interface** + `Pipeline.Resolvers
  map[string]Resolver` — replaces the per-resolver field pile. Legacy
  `GoResolver` field still honored for backward compatibility.
- **Three new integration-test cases** — `v1.3_ts_this_method_resolution`
  (AuthService.issueToken → this.fingerprint lands as a resolved ref),
  `v1.3_python_self_method_resolution` (JobQueue.drain → self.dequeue),
  `v1.3_no_truly_unresolved_refs` (all TS + Python calls in the fixture
  are visited and stamped).

### Explicit non-goals (stays textual)

- TS: generics, conditional types, declaration merging, ambient modules
  beyond `tsconfig.paths`, arbitrary `obj.method()` that needs type
  inference.
- Python: `super()` chain resolution, `getattr(obj, 'm')(...)` dynamic
  attribute access, type-based method dispatch.

### Fixture additions

- `testdata/fixtures/sample/src/auth.ts` grew `normalizeEmail`,
  `issueToken`, `fingerprint` — together they exercise cross-module
  imports, `this.`-calls, and cross-function linking within a class.
- `testdata/fixtures/sample/py/worker.py` grew `drain` — exercises
  `self.`-calls and param-typed calls we deliberately don't resolve.

### Self-index unchanged

The self-index already hit 0.0% unresolved in v1.2 (pure Go repo).
v1.3 additions keep it there: 66 files, 454 symbols, 2488 refs, 0
resolution-bug self-loops, 0 truly-unresolved non-import refs.

## [Unreleased (v1.2 hotfixes)]

- **`LIMITATIONS.md`** at repo root — single source of truth for what
  doesn't work today, grouped by cause (resolution quality, graph queries,
  indexing/scale, distribution, tooling surface). Linked from README and
  CLAUDE.md. Edit on every milestone.
- **Depth-clamp surfaces a note** — requesting `get_neighborhood` with
  depth > 5 now returns a `notes` entry on the result explaining the
  clamp and pointing at LIMITATIONS.md. Visible in the CLI (stderr),
  HTTP, and MCP responses. Silent clamp was too easy to miss.

## [v1.2.0] — 2026-04-22

"Go, but honest" — the second v2.0 milestone (Pillar A for Go). Type-aware
reference resolution kills the self-loop class of resolution bugs and pushes
the unresolved-ref ratio on mycelium's own repo from 74.8% to 0%.

### Added

- **`internal/resolver/golang`** — Go type resolver built on
  `golang.org/x/tools/go/packages` + `go/types`. Loads the whole module
  once, walks each file's AST using the cached `*types.Info` side tables,
  and rewrites call-ref `DstName` into the same `pkg.Receiver.Method`
  shape the parser uses for its own symbols. Stamps every visited call
  with `ResolverVersion=1` regardless of whether it could rewrite the
  name, so builtins/conversions/erased-receiver calls are correctly
  classified as "analyzed, no local target" rather than "unknown."
- **Migration `0004_resolver_version.sql`** — `refs.resolver_version`
  column + index. 0 = textual, 1 = go-types resolver, 2+ reserved for TS
  (v1.3) / Python (v1.3).
- **Honest metrics** in `query.Stats` — `NonImportRefs`, `RefsTypeResolved`,
  `RefsExternalKnown`, `RefsTrulyUnresolved`, `RecursionSelfLoops`.
  `UnresolvedRatio()` now measures genuine unresolved-ness (v0 + no link,
  non-import), not "dst_symbol_id IS NULL" (which lumped stdlib calls in
  as "failures").
- **`MYCELIUM_RESOLVER_DEBUG=1`** env var — per-file resolution counts on
  stderr for diagnosing edge cases without a rebuild.

### Changed

- SQL resolver's unique-short-name fallback is now **v0-only**. Refs the
  type-aware pass visited skip the ambiguity-prone fallback, eliminating
  the self-loop class (e.g. `ix.db.Close()` no longer resolves to our
  `Index.Close`).
- `self_loop_count` now counts only resolution-bug self-loops (v0);
  genuine recursion (v1) is reported separately as `recursion_self_loops`.
- `Tests: true` in the `packages.Config` — integration and bench test
  files are now part of the type graph.
- Go `go` directive bumped to 1.25.0 (required by `golang.org/x/tools`).

### Self-index baselines (Tiger Lake laptop, `myco doctor`)

| metric | v1.1 | v1.2 |
|---|---|---|
| self_loop_count (bugs) | 11 | **0** |
| recursion_self_loops (informational) | n/a | 12 |
| unresolved_ref_ratio | 74.8% | **0.0%** |
| refs_resolved_local | 556 | 550 |
| refs_external_known | n/a | 1425 |
| doctor exit code | 2 (fail) | 0 (pass) |

### Benchmarks (10k synthetic Go symbols, Tiger Lake)

| op | v1.1 | v1.2 |
|---|---|---|
| initial index | 2433 sym/sec | 2347 sym/sec (−3.5%) |

Note: benchmark fixtures don't carry a `go.mod`, so the resolver is nil in
this measurement. The resolver adds a fixed one-time cost per Pipeline
construction for the `packages.Load` call (~200ms on the self-index).

## [v1.1.0] — 2026-04-22

First milestone on the v2.0 roadmap ("Honest signals"). Adds health checks
so later milestones can measure themselves against honest baselines.

### Added

- **`myco doctor`** subcommand with per-check Pass/Warn/Fail output and
  conventional exit codes (0/1/2). `--json` flag for CI.
- **`internal/doctor`** package — configurable thresholds, pluggable into
  future MCP introspection.
- **Extended `stats`** — `self_loop_count`, `unresolved_by_language`,
  `total_refs_by_language`, `stale_chunks`, `embed_queue_depth`, DB size and
  fragmentation, plus `UnresolvedRatio()` / `DBFragmentation()` helpers.
- **Benchmark harness** — `GenerateSyntheticRepo()` emits deterministic
  Go-only fixtures at arbitrary symbol counts. Benchmarks for initial index,
  `FindSymbol`, and `GetNeighborhood` depth-2. Baselines at 10k symbols on
  a Tiger Lake laptop: **2433 sym/sec**, **11.4 ms** point lookup, **3.8 ms**
  neighborhood query.

### Baselines captured

Self-index of mycelium under provider=none:

- 57 files · 387 symbols · 2045 refs
- self_loop_count: **11** (Pillar A in v1.2 targets 0)
- unresolved_ref_ratio: **72.8%** (Pillar A target <8% for Go)
- db_fragmentation: 11.1%

## [v1.0.0] — 2026-04-22

First stable release. Nine MCP tools, three transports, three languages.

### Added

- **Release binaries.** GitHub Actions matrix build for `linux/amd64`,
  `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`. Version
  injected via `-ldflags "-X main.version=…"`.
- **Integration test.** Committed multi-language fixture
  (`testdata/fixtures/sample`) exercised end-to-end in CI: parsers, index,
  all nine query methods.
- **CI.** Lint + vet + race-enabled tests on Linux and macOS.

## [v0.5.0] — 2026-04-21

### Added

- **`search_lexical`** — parallel 4-worker ripgrep-style regex scan over
  indexed files. Fills the gap where semantic search misses exact strings.
- **`get_file_summary`** — structural summary per file: exports, imports,
  LOC, symbol counts by kind. No LLM calls.
- **`get_neighborhood`** — local call graph around a symbol via recursive
  CTE on `refs`. Depth capped at 5; direction = out | in | both.
- **HTTP transport** — loopback server on `127.0.0.1:<http_port>`. Routes:
  `POST /rpc` with `{method, params}` and per-method `POST /<method>`.
- **Parallel initial scan** — worker pool for parsing; single-writer
  goroutine for DB commits. Threshold-gated (≥200 files) to avoid
  goroutine overhead on small repos.

## [v0.4.0] — 2026-04-21

### Added

- **Semantic search** (`search_semantic`) — embeds the query, brute-force
  cosine similarity over stored float32 vectors. Top-k with snippet,
  kind/path filtering.
- **Embedders.** `Noop` (default), `Ollama` (local `http://localhost:11434`),
  `Fake` (test-only). Pluggable via `.mycelium.yml`.
- **Chunker.** One chunk per symbol with qualified name + signature +
  docstring + body; skips tiny const/var without docstrings.
- **Embed queue + worker.** Background goroutine in the daemon; batches to
  the embedder, writes to `chunks.embedding` + `embed_cache`. Rate-limit
  circuit breaker (trailing 60s).
- **Model-switch invalidation.** Changing `embedder.model` on daemon start
  drops stale vectors automatically.

### Changed

- Migrated chunks table to include `content`, `embedding`, `embed_model`
  columns (migration `0002_embeddings.sql`). Deferred `sqlite-vec` —
  brute-force Go cosine is fast enough for typical repos.

## [v0.3.0] — 2026-04-21

### Added

- **MCP stdio server** (`myco mcp`) — minimal JSON-RPC 2.0 over stdio, no
  external MCP SDK. Exposes five tools: `find_symbol`, `get_references`,
  `list_files`, `get_file_outline`, `stats`.
- **`myco init`** — writes `.mycelium.yml`, adds `.mycelium/` to
  `.gitignore`, installs post-commit hook, prints Claude Code / Cursor MCP
  config snippet via `--mcp claude|cursor`.
- **Post-commit git hook** — reconciles the index after commits when the
  daemon isn't running.
- **TypeScript/TSX parser** — `smacker/go-tree-sitter` grammar; extracts
  function / class / interface / type / enum / var / method / field decls
  plus import + call refs. Leading `_` heuristic for private.
- **Python parser** — tree-sitter grammar; extracts function / class /
  method decls with PEP-257 docstring detection. `_`-prefix convention for
  private; dunders are public.
- **Shared tree-sitter helpers** (`internal/parser/tsutil`) — slice, position,
  walk, preceding-comment extraction.

## [v0.2.0] — 2026-04-21

### Added

- **Daemon** (`myco daemon`) — long-running per-repo process that owns the
  index. Thin clients (CLI, MCP, hook, HTTP later) talk to it via a unix
  socket at `.mycelium/daemon.sock`.
- **fsnotify watcher** — recursive watch with per-file debounce window;
  auto-registers new directories.
- **Reference resolution pass.** Two-step: exact qualified match, then
  unique short-name match via `refs.dst_short` column. `ON DELETE SET NULL`
  cascades keep refs honest.
- **`get_references`, `list_files`, `get_file_outline`** query methods.
  Refs flag each hit as `resolved` vs `textual`.
- **Query package** (`internal/query`) — the single reader of the DB.
  All transports call this package.

## [v0.1.0] — 2026-04-21

Initial indexer. Go-only. One-shot CLI.

### Added

- **Go parser** — stdlib `go/ast`, no cgo. Extracts functions, methods,
  types (struct / interface / alias), top-level vars / consts, imports,
  call-site refs.
- **SQLite schema** (`migrations/0001_init.sql`) — files, symbols, refs,
  chunks, `symbols_fts` (FTS5 trigram), `embed_cache`, `embed_queue`, meta.
- **Walker** (`internal/repo`) — doublestar-matching include/exclude, size
  limits, `.git` / `.mycelium` skipping.
- **One-shot pipeline** — hash-gated per-file transactions.
- **`myco index`, `myco query find`, `myco stats`** subcommands.
