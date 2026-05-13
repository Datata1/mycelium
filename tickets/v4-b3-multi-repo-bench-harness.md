# B3 — Multi-repo `bench-counterfactual` harness

**Priority:** P1 for v4 Phase 1 — gate for the v3.4 A3 model's credibility
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Depends on:** v3.4 Block A (bench-counterfactual subcommand — already shipped)

## Goal

Today `myco bench-counterfactual` has a hard-coded corpus pointing at
mycelium self-index symbols (`ComputeSessionCost`,
`internal/telemetry/aggregate.go`). The four calibrated multipliers
in `internal/telemetry/counterfactual.go` are therefore *Go-codebase
specific*. The v3.4 G3 finding warned this is fragile — a Python
codebase has different doc-comment density and would measure
different ratios for `get_file_outline` / `get_file_summary`.

This ticket extracts the corpus interface so:

- `myco bench-counterfactual --repo /path/to/other/repo` runs against
  any indexed repo with a per-language default corpus.
- Per-language overrides land in the multiplier table when the
  bench shows a stable difference (e.g. Python `get_file_outline`
  multiplier might be 1.5× while Go's stays at 2.5×).

After this ticket: the savings model is honestly representative of
the user's codebase, not just mycelium's.

## What changes

### Corpus interface

```go
// internal/bench/corpus.go (new file)
type Corpus struct {
    Cases []Case
}

type Case struct {
    Tool         string
    Method       string
    Params       any
    FallbackCmd  string  // run via bash -c, stdout bytes measured
    FallbackFile string  // os.Stat measured (mutually exclusive with FallbackCmd)
    Note         string
}

// Per-language defaults, picked by the dominant language in the
// indexed repo (myco stats → ByLang).
func DefaultCorpus(language string, root string) Corpus { ... }
```

`go` returns the current mycelium corpus.
`typescript` returns a TS corpus (likely picks a `useState`-style
hook + a top-level `tsx` component file).
`python` returns a Python corpus (`Django.urls.path` + a Django
view file, or `flask.route` for Flask).
`unknown` returns an empty corpus + clear error: "no default corpus
for language X; pass --corpus-file <path>".

### Per-language multipliers

```go
// internal/telemetry/counterfactual.go
type counterfactualEntry struct {
    multiplier float64
    quality    EstimateQuality
    perLang    map[string]float64 // optional override per dominant language
}
```

`EstimateCounterfactual(tool, outputBytes)` stays unchanged in
signature. `EstimateCounterfactualFor(tool, outputBytes, language)`
is new — uses the per-language override if present, else falls back
to `multiplier`. `ComputeSessionCost` accepts a new `language string`
parameter (or reads `Stats.DominantLanguage()`); the existing
no-language entry-point keeps the Go default for backward compat.

### CLI

- `myco bench-counterfactual --repo <path>` overrides repo root.
- `myco bench-counterfactual --corpus-file <yaml>` lets users
  bring their own.
- `myco bench-counterfactual --update-multipliers` writes the
  measured ratios into a per-language override block (with a
  confirmation prompt; doesn't touch the high-quality Go defaults
  unless drift > threshold).

## Critical files

- `cmd/myco/main.go` — extract `benchCorpus` into the new
  `internal/bench` package; flag plumbing.
- `internal/bench/corpus.go` — new file, the corpus interface +
  per-language defaults.
- `internal/telemetry/counterfactual.go` — `perLang` map field;
  new `EstimateCounterfactualFor` signature.
- `internal/telemetry/aggregate.go` — `ComputeSessionCost` plumbs
  language through.
- `internal/telemetry/calibration_test.go` — extend pinned test
  to cover per-language overrides when present.

## Acceptance criteria

- `task check` passes.
- `myco bench-counterfactual` with no flags reproduces the v3.4
  table (regression-clean).
- `myco bench-counterfactual --repo <fresh-go-repo>` runs without
  daemon errors and shows results within a few percent of the
  self-index numbers.
- `myco bench-counterfactual --repo <fresh-typescript-repo>`
  works against a TS-dominant repo with a TS corpus.
- New unit test: `internal/bench/corpus_test.go` covers
  `DefaultCorpus("python", root)` returning a non-empty corpus.
- Per-language multipliers, when populated, override the default
  in cost calculations — pin in
  `TestCounterfactualModel_PerLanguageOverride` (calibration test).
- `myco bench-counterfactual --update-multipliers` is documented
  in the bench command's `--help` and asks before mutating
  `counterfactual.go` (since editing source from a CLI command is
  unusual; the prompt is the safety net).

## What this enables

- **Per-repo honesty.** A Python user gets Python-tuned numbers,
  not Go-tuned numbers. The savings line stops being misleading.
- **Calibration in v4 Phase 2 field tests.** F1 (Django) and F2
  (Axum) both run B3 against their target repo as part of their
  acceptance — the per-language multiplier table fills in.
- **Foundation for community-contributed corpora.** A user with a
  Spring repo can drop a `mycelium-corpus.yaml` and get sane
  numbers without us shipping Java support yet.

## Out of scope

- **Auto-detecting the dominant language.** B3 reads
  `Stats.ByLang` and picks the largest. Users with truly polyglot
  repos can pass `--corpus-file`; the dominant-language heuristic
  is fine for the 80% case.
- **Bench against an unindexed repo.** Requires the repo to be
  indexed first (`myco index` with an existing or fresh
  `.mycelium.yml`). Document the prerequisite in `--help`.
- **Live A/B mode.** Still v5+. B3 stays in the modelled-bench
  paradigm.

## Honest caveats

- Picking representative corpus targets per language is itself
  a calibration problem. The TS corpus might pick "well-typed
  React hook" while reality is "any TSX in /pages with mixed
  HTML/TS noise" — the multipliers will land somewhere between.
  Document the corpus picks in `corpus.go` with reasoning so
  future tuning is informed.
- `--update-multipliers` is a code-mutation by a CLI command.
  The confirmation prompt + git-diff visibility is the safety
  net. If this feels too magical, drop it from v4 and require
  manual table edits — it's a convenience, not load-bearing.
