# F1 — Python/Django field test (non-TS prereq)

**Priority:** P1 for v4 Phase 2 — gates C1 (route literals) for Python frameworks
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Depends on:** v4-b3 (multi-repo bench harness — to populate Python multipliers)

## Goal

The v3.1 plan and the v3.4 G3 finding both name a non-TS field test
as the gate for v3.4 (now v4) work on route literals + new languages.
Today we have one Go data point (mycelium dogfooding) and one TS
data point (monorepo-4). Python/Django is the natural third: a major
framework with route patterns (`path("/foo", view)`), externalised
strings (Django i18n), and a parser/resolver mycelium *already
ships* — so this test is "validate existing Python support" rather
than "add a new language".

After this ticket: a written `tickets/v4-f1-findings.md` documenting
findings as `Pn` blocks (Python-prefixed, mirroring the `Fn` Codesphere
and `Gn` Go format), plus a populated Python entry in the per-language
multiplier table from B3.

## What this is (process, not code)

A *manual* field test — not a unit test, not a CI job. One agent
session against a real-world Django repo, with myco wired into the
agent's MCP, doing a representative coding task. Findings then drive
C1's `route_constructors` config and any Python parser fixes that
fall out.

## Setup

1. Pick a Django repo. Recommended candidates (in order):
   - `django/django` itself — large but well-known, exercises the
     ORM + admin + i18n surfaces.
   - `wagtail/wagtail` — CMS, mid-size, heavy admin UI patterns.
   - `cookiecutter-django` template applied to a fresh project plus
     a couple of apps — small but representative of "real user code".
2. Clone, `myco init` interactively, accept defaults. Confirm
   `telemetry.enabled: true` lands in `.mycelium.yml` (post-v3.2 default).
3. `myco index` → `myco doctor`. Capture the doctor output verbatim
   in the findings doc.
4. Wire myco into Claude Code MCP per `docs/adoption.md`.

## The task to attempt

**Pick a real coding task** — one of:

- **Add a new view + URL route + template.** Mirrors the
  monorepo-4 F4 finding (route literals invisible).
- **Add a new i18n key + translation.** Mirrors monorepo-4 F3
  (JSON config invisible — though Django uses `.po`, not JSON;
  this surfaces the v3.3 documents-surface gap for Django).
- **Refactor an existing view to use a new helper.** Generic
  navigation task; exercises `find_symbol` / `get_references`.

The task should take ~30-60 minutes in a single session. Document
the prompt verbatim in the findings doc.

## What to capture

For each tool the agent reaches for (myco MCP **and** fallback
Bash/Read/Edit), capture:

- Tool name + arguments
- Whether the agent could have used a myco tool instead (and which one)
- Whether myco's output was *useful* (resolved the question) or
  required a follow-up call

After the session, run:

- `myco session export <id> --format markdown` — paste into findings doc.
- `myco bench-counterfactual --repo .` — run the B3 harness against
  this repo. Capture the per-tool table.
- `myco doctor --window 24h` — paste the adoption-health output.

## Findings document format

Write `tickets/v4-f1-findings.md`. Use this skeleton:

```markdown
# F1 findings — Python/Django field test

**Repo:** <name>, <commit hash>
**Task:** <one-paragraph description>
**Session:** ses_<id>
**Date:** <date>

## Executive summary

<3-5 bullet points of what worked / what didn't>

## P1. <finding name> [strong|medium|speculative]

<paragraph + concrete evidence — tool calls, line numbers, snippets>

## P2. ...

## Routes data point

Did `path("/foo", view)` calls show up in the agent's grep fallbacks?
List every URL pattern the agent searched for textually — that's
direct evidence for/against C1 (route literals as symbols).

## Documents data point

Did `.po` / `settings.py` / `pyproject.toml` files surface as
needed-but-not-indexed? List them.

## Bench-counterfactual results

<paste table from `myco bench-counterfactual --repo .`>

## Recommendation for C1 / C2

<one paragraph: which framework constructors C1 should support,
which Python parser fixes are needed, whether Python should be
the v4 new-language pick (probably no, since it's already supported)>
```

## Acceptance criteria

- `tickets/v4-f1-findings.md` exists, follows the skeleton, has
  ≥ 3 `Pn` findings (more is better).
- The findings include a verbatim bench table for the test repo.
- The `routes data point` section names every URL pattern the
  agent searched for textually — input data for C1's
  `route_constructors` config.
- The recommendation closes the speculation in the v4 plan
  about which Django constructors matter.
- The Python multiplier entries land in
  `internal/telemetry/counterfactual.go` via B3's
  `--update-multipliers` flow (or manually if B3 isn't ready
  yet — flag the dependency).

## What this enables

- **C1 ships with confidence** that Django routes are real signal,
  not speculation.
- **C2 deprioritises Python** as the new-language pick (Python is
  already supported), redirecting attention to Rust or Java.
- **The Python multipliers stop being mycelium-Go-tuned**, fixing
  the v3.4 G3 honesty gap.

## Out of scope

- **Adding any code.** F1 is observation-only. Bug reports the
  test surfaces become separate tickets.
- **Multiple Django versions** — pick one Django version, pin in
  findings. Cross-version coverage is not the question.
- **Comparing to non-Django Python frameworks.** Flask/FastAPI are
  worth their own field test if Django reveals
  framework-specific patterns; out of scope here.

## Honest caveats

- One session = one data point. Two findings docs from the same
  Django repo doing different tasks would be more robust; one is
  the budget.
- The agent doing the field test (likely Claude Code) is the same
  agent the patterns are tuned for. If a different agent shows
  different reflexes, the findings don't generalise. Document the
  agent + version explicitly.
