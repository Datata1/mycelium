# F2 — Rust/Axum field test (new-language candidate)

**Priority:** P1 for v4 Phase 2 — gates C2 (new language pick)
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Depends on:** v4-b3 (for the bench harness against Rust)

## Goal

Validate that Rust deserves the v4 new-language slot — by observing
how an agent uses (and fails to use) myco on a real Rust/Axum
codebase *without* Rust parser support. The contrast tells us
whether the existing tools (find_symbol on text-only Rust files,
search_lexical, read_focused) cover the 80% case or whether the
agent immediately falls back to grep for everything.

This is a different shape of field test from F1: F1 validates
*existing* support; F2 tests *what the agent does without
support*. The output drives C2's decision: ship Rust as the new
language, or pick a different one based on what the test reveals.

## What this is (process)

Manual field test. One agent session against a real-world Rust/Axum
repo, with myco wired in but no Rust parser. The agent does a real
task. We observe.

## Setup

1. Pick a Rust/Axum repo. Recommended candidates:
   - `tokio-rs/axum` examples (`examples/sqlx-postgres`,
     `examples/key-value-store`) — small, clearly framework-shaped.
   - `meilisearch/meilisearch` — large, real product, lots of
     route definitions in a single file.
   - A self-built minimal Axum app (~5 routes, 1 handler each) —
     smallest-possible test surface; useful if the candidates
     above are too big.
2. Clone, `myco init` interactively. Document what mycelium does
   when it encounters `.rs` files — does it skip them, log a
   warning, treat them as text? Capture verbatim.
3. `myco index` → `myco doctor`. Note any Rust-specific errors
   (likely "language: rust unsupported, files not indexed").
4. Wire into Claude Code MCP.

## The task to attempt

**Pick a real coding task** — one of:

- **Add a new route + handler.** Mirrors monorepo-4 F4 + the
  Django F1 task; tests the route-literal pattern in
  `Router::new().route("/foo", get(handler))` form.
- **Trace a request through middleware.** Tests cross-file
  navigation (`find_symbol` on a middleware function name) on
  a language mycelium doesn't parse.
- **Refactor a handler to extract a helper.** Generic
  navigation; exercises `get_references` against textual
  fallback.

Same shape as F1: ~30-60 min, single session, verbatim prompt
in findings doc.

## What to capture

For each tool the agent reaches for, capture:

- Tool name + arguments
- Did myco return useful results despite no Rust parser? (Some
  myco tools are language-agnostic — e.g. `read_focused` works
  on any text file once it's tracked.)
- When the agent fell back to grep/Read, was it because:
  - myco doesn't support the language at all (filed under: needs C2)
  - myco supports the surface but the agent didn't reach for it
    (filed under: needs CLAUDE.md priming or B2 doctor warning)
  - the surface genuinely doesn't exist in myco (filed under:
    new tool surface needed, possibly v4.1+)

After the session, run:

- `myco session export <id> --format markdown`.
- `myco bench-counterfactual --repo .` — even with Rust unparsed,
  the bench tools that don't depend on Rust symbols (e.g.
  `search_lexical`, `list_files`) will give numbers. Capture them.
- `myco doctor --window 24h`.

## Findings document format

Write `tickets/v4-f2-findings.md`. Skeleton:

```markdown
# F2 findings — Rust/Axum field test

**Repo:** <name>, <commit hash>
**Task:** <description>
**Session:** ses_<id>
**Date:** <date>

## Executive summary

<3-5 bullets>

## R1. <finding> [strong|medium|speculative]

## R2. ...

## What myco couldn't do (Rust-parser-shaped gaps)

List concrete cases: "agent searched for `fn handle_foo` and got
0 results from `find_symbol` because Rust files aren't indexed."
Each entry is direct evidence for C2's scope.

## What myco *could* do (language-agnostic surfaces that worked)

`search_lexical`, `list_files`, `read_focused` (post-B1) work on
any text file. Did the agent discover and use them?

## Routes data point

How many times did the agent search for `Router::new().route(...)`
patterns textually? Direct evidence for C1's Rust route_constructors
config (when C2 lands).

## Bench-counterfactual results

<paste table — even partial — from B3 against this repo>

## Recommendation for C2

<one paragraph: should v4 new-language be Rust or something else?
If Rust, what's the minimum viable parser scope (functions + structs
+ use statements? Or also impl blocks + trait methods)? If not
Rust, what should it be (Java/Spring? C/C++?) and why>
```

## Acceptance criteria

- `tickets/v4-f2-findings.md` exists, follows the skeleton, ≥ 3
  `Rn` findings.
- The "what myco couldn't do" section names ≥ 5 specific failures
  attributable to no Rust parser — input for C2's scope.
- The bench table is captured, even if some rows show
  language-unsupported errors (those are findings).
- The recommendation gives a concrete answer to "should C2 ship
  Rust" with reasoning. If not Rust, names the alternative.

## What this enables

- **C2 has a clear target.** Either Rust + concrete scope, or a
  different language with explicit reasoning.
- **C1's Rust support** (the second framework after Django/Python)
  ships with framework constructor evidence rather than speculation.
- **v4.1+ language candidate ranking** — F2's "what couldn't be
  done" list is the cost side of the decision for the next
  language after v4.

## Out of scope

- **Implementing any Rust parsing** — F2 is observation. C2 is
  implementation.
- **Multiple Rust web frameworks** — Axum is the pick (most
  modern + active). Actix / Rocket are different tickets if F2
  surfaces value in covering them.
- **Comparing to non-Rust systems languages** (C++, Zig). Out of
  scope; pick after F2 if Rust isn't the right call.

## Honest caveats

- The agent's reflex on a non-supported language is the worst-case
  baseline — they grep / Read everything. Findings will probably
  show "myco didn't help much", which is the *expected* answer
  and the input for C2's value calculation.
- If the agent never tries `find_symbol` on Rust (knowing it's
  unsupported), we don't see whether they'd want it. Mitigation:
  the F2 prompt should be neutral ("explore this repo and add a
  route"), not "use myco for everything" — let the agent choose.
