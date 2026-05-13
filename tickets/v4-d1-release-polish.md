# D1 — v4.0 release polish

**Priority:** P0 for v4 Phase 4 — the actual release cut
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Depends on:** B1, B2, B3, F1, F2, C1, C2 (everything else)

## Goal

Cut v4.0. Convert the work in Phases 1-3 into a tagged release that
users can install via the existing GitHub Releases distribution
path, with documentation and limitation notes accurate to the
shipped state.

This ticket is **process-heavy, code-light**. Most of it is
syncing docs and CHANGELOG. The new code is one small
release-script change.

## What changes

### CHANGELOG

A `## [v4.0.0] — <date>` section that consolidates the v4 phases
into Keep-a-Changelog form. Use this skeleton:

```markdown
## [v4.0.0] — <date>

### Added

- **read_focused without focus now returns a useful preview** (B1) —
  previously a no-focus call returned the full file with non-matching
  symbols collapsed (which collapses nothing), making it net-heavier
  than a plain Read. Now returns outline + first N lines + a hint
  to pass `focus=`. Removes the v3.4 A3 G2 net-negative case.

- **Adoption-health doctor checks** (B2) — `myco doctor` now reads
  recent session telemetry and warns on the three documented
  failure modes (search_lexical-only, Read-over-read_focused,
  grep-over-myco). Configurable window via `--window <duration>`.

- **Multi-repo bench-counterfactual** (B3) — bench corpus is now
  pluggable. `myco bench-counterfactual --repo <path>` runs
  against any indexed repo with a per-language default corpus
  (Go, TS, Python, Rust). Per-language multipliers populate when
  measured ratios stably differ from the Go-self-index defaults.

- **Route literals as symbols** (C1) — framework route registration
  patterns (`Django.urls.path`, `axum.Router.route`,
  `@tanstack/react-router.createFileRoute`, etc.) emit
  `kind: "route"` symbols. New `find_route(pattern)` MCP tool.
  Configurable via `languages.<lang>.route_constructors:`. Default
  off until users opt in. Closes the v3.1 F4 finding.

- **<NEW LANGUAGE> language support** (C2) — fourth supported
  language after Go / TS / Python. ResolverVersion bumped to 4.
  See LIMITATIONS.md for the v4-cut scope (in: functions, types,
  use/import statements, method calls; out: macro expansion,
  generics-aware resolution).

### Field tests

- **F1 — Python/Django.** Validated existing Python support;
  populated Django route_constructors defaults. See
  `tickets/v4-f1-findings.md`.

- **F2 — <Rust/Axum or chosen language>.** Drove the C2 language
  pick + scope. See `tickets/v4-f2-findings.md`.

### Changed

- **Counterfactual model recalibration.** Multipliers now have
  per-language overrides where measured ratios diverged from the
  Go-self-index defaults. Existing sessions continue to estimate
  with the language-default multiplier; aggregator picks the
  override automatically when `Stats.DominantLanguage` matches.

### Removed

- Nothing removed in v4. All v3.x APIs remain.

### Migration notes

- No SQL migration required; v4 reuses the v3.x schema. Re-indexing
  is unnecessary unless users want C2's new language support to
  pick up `.rs` (or chosen-language) files that were previously
  ignored.
- `route_constructors` config defaults to empty — users opt in
  per-framework. See `docs/route-config.md` for copy-paste blocks.
```

### README

- Update the supported-languages list (add the C2 language).
- Add a "find_route" example to the MCP tool overview.
- Update the bench numbers if v4 dogfooding produced fresher data.

### LIMITATIONS.md

- Move "route literals invisible" out of the limitations list (now
  shipped as C1).
- Add "macro expansion in Rust" to the resolution-quality section
  (per C2's honest caveats).
- Update the v3 references to v4 where relevant.

### CONTEXT.md

- One-paragraph addition to the "shipped" section: v4 closes the
  agent-native + cost-conscious story; v5+ is federation territory.

### docs/

- New `docs/route-config.md` — per-framework copy-paste config
  blocks for Django, FastAPI, Axum, TanStack Router, Next.js,
  any others F1/F2 surfaced. One section per framework, ~10 lines
  each.
- Update `docs/adoption.md` if B2's WARN messages reference
  specific anchor IDs.
- Update `docs/navigation-example.md` to include a `find_route`
  call in the example flow.

### Distribution

- `myco --version` should print `v4.0.0` once tagged.
- GitHub release artifacts: macOS arm64, macOS amd64, linux amd64,
  linux arm64, Windows amd64. Bundle sqlite-vec extension binary
  (already done since v3.0). Bundle tree-sitter-rust grammar
  binary (new for v4).
- Test the release tarball end-to-end: download, untar, run
  `myco init`, `myco doctor` on a fresh repo. Document any
  rough edges as v4.0.1 candidates.

## Critical files

- `CHANGELOG.md` — the section above.
- `README.md` — supported-languages, find_route example.
- `LIMITATIONS.md` — the row updates.
- `CONTEXT.md` — the one-paragraph addition.
- `docs/route-config.md` — new file.
- `.github/workflows/release.yml` (or whatever the release wf is) —
  add the tree-sitter-rust grammar to the bundled artifacts.
- `cmd/myco/main.go` — `version` var; `-ldflags` injection in
  the release script. Confirm the existing scheme picks up `v4.0.0`
  cleanly.

## Acceptance criteria

- `git tag v4.0.0` lands on a clean main with no failing CI.
- A user can run `myco init` on a fresh repo with a v4.0.0
  binary, accept defaults, and see `myco doctor` report green
  including the new adoption-health section (with the
  "no telemetry yet" friendly message).
- The GitHub release page lists v4.0.0 with all platform binaries
  and the bundled tree-sitter-rust grammar.
- `find_route` works out of the box on a fresh Django project
  *after* the user adds the example `route_constructors:` block
  from `docs/route-config.md`.
- README's bench/savings numbers are within ±10% of the v3.4 A3
  numbers (they should be — v4 changes the model less than v3.4
  did).
- LIMITATIONS.md no longer lists "routes invisible".
- The CHANGELOG entry is in Keep-a-Changelog form with explicit
  Added / Changed / Removed sections.

## What this enables

- **Users can adopt v4 without reading the v4 plan.** The
  CHANGELOG + README + docs/route-config.md cover the new
  surfaces.
- **`myco init` on a fresh Rust repo** does the right thing
  (indexes `.rs` files, suggests Axum config in
  docs/route-config.md).
- **The v4 release is the cut-point** for the v3.x → v4 jump in
  documentation; v3.x users upgrading get an explicit migration
  paragraph.

## Out of scope

- **v4.1 planning.** Don't pre-spec v4.1 — same lesson as the v3.1
  plan's "don't over-spec v3.2 / v3.3 / v3.4 now". Pick v4.1 scope
  after v4 ships and field-test data lands.
- **Marketing.** Blog posts, social posts, etc. — out of scope.
  Release notes alone.
- **Backporting v4 fixes to a v3.x maintenance branch.** Mycelium
  doesn't maintain LTS branches; v4 is the new mainline.

## Honest caveats

- The CHANGELOG skeleton has `<date>` placeholders. Fill at tag
  time, not draft time.
- Distribution testing (download → untar → run) is manual. CI
  builds the binaries; no CI test exercises them as a user
  would. Worth a 10-minute manual pass on macOS arm64 + linux
  amd64 before declaring the release shipped.
- The bench/savings numbers in the README will rot. v4.1+ work
  could add a `task readme-numbers` to regenerate them from a
  fresh dogfood session — not v4 scope, but a tracked TODO.
