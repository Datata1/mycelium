# Refactoring Roadmap — Professionalizing mycelium

Seven workstreams, one plan document each. Goal: make future features cheaper to
build, the code easier to understand, and the repo safe and welcoming for
external contributors.

Sizes: **S** < 1 day, **M** 1–3 days, **L** 3–5+ days.

## Status (2026-07-03)

Implemented: 01, 02, 06, 07-A, 03 (DTO move + render decoupling), 04, 05.
Open follow-ups:

- **03**: the query-internal store extraction (SQL into an unexported
  `store`, pure ranking/hint/preview functions) — the DTO move landed;
  the god-file split is trailing work, unblocked and incremental.
- **04**: the dual-path equivalence suite (every query subcommand with
  daemon up vs. down) needs unix-socket binding, which the local sandbox
  forbids — implement alongside 07-C with the skip-on-EPERM pattern used
  in `internal/daemon/daemon_test.go`.
- **07 B/C**: query store tests (after the 03 extraction), httptest for
  the HTTP transport.
- **01**: first CI lint run may surface findings golangci-lint could not
  be executed locally (sandbox network); fix-forward.

## Dependency graph

```
01 oss-hygiene ────────────────────────────────┐ independent
02 errors-logging ─► 03 wire-types-query ─► 04 service-layer ─► 05 tool-registry
06 language-unification ───────────────────────┘ independent (ideally before 04)
07 tests: phase A anytime ─ phase B after 03 ─ phase C after 04/05
```

## Execution waves

| Wave | Workstreams | Notes |
|------|-------------|-------|
| 1 | 01, 02, 06, 07-A | All parallelizable; no mutual conflicts. |
| 2 | 03, then 04; 07-B trails 03 | 03's DTO move is small and unblocks 04 early. |
| 3 | 05, 07-C | 05 is the payoff: six tool-definition sites collapse to two sources of truth plus one table. |

## Workstreams

| # | Plan | Size | Depends on | Blocks |
|---|------|------|-----------|--------|
| 01 | [oss-hygiene](01-oss-hygiene.md) — LICENSE, module path fix, CONTRIBUTING, lint, CI | S/M | — | — |
| 02 | [errors-logging-foundations](02-errors-logging-foundations.md) — sentinels, wire error codes, slog, typed strings, ctx | M | — | 03, 04, 05 |
| 03 | [wire-types-query-decomposition](03-wire-types-query-decomposition.md) — DTOs to ipc, Reader → store + pure logic | M/L | 02 | 04, 05, 07-B |
| 04 | [service-layer-single-dispatch](04-service-layer-single-dispatch.md) — internal/service, kill the CLI dual path | M/L | 02, 03 (DTO move) | 05, 07-C |
| 05 | [tool-registry](05-tool-registry.md) — one table + parity tests replaces four switches | M | 03, 04 | — |
| 06 | [language-unification](06-language-unification.md) — internal/languages, delete Go special-case, fix docs | S/M | — | — |
| 07 | [test-suite-buildout](07-test-suite-buildout.md) — unit tests + golden files under the integration suite | L | phased | — |

## Decisions already made (repo owner, 2026-07-03)

- **License: Apache-2.0.**
- **CLI keeps working without a daemon** — dual path collapses into a shared
  `internal/service` layer, not daemon-autostart.
- **Module path moves to `github.com/datata1/mycelium`** to match the actual
  remote (`go.mod` currently says `jdwiederstein`, which is wrong).
- **Logging: stdlib `log/slog`** replaces the two duplicate Printf interfaces.

## Non-negotiable constraints (apply to every workstream)

- Daemon stays the only SQLite writer; `internal/query` the only reader,
  `internal/pipeline` the only writer; transports never issue raw SQL.
- Parsers emit plain structs and know nothing about storage; the Go parser
  stays stdlib `go/ast` (no cgo for Go).
- Migrations are additive only. No pre-commit hooks. Distribution stays
  GitHub-Releases binaries only.
- Every build/test/lint command carries `-tags sqlite_fts5`.
- Every new query method gets an integration test case; benchmarks guard the
  query hot path.
