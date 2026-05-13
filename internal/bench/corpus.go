// Package bench is v4 B3's extraction of the bench-counterfactual
// harness from cmd/myco/main.go. The point is making the corpus
// pluggable so the calibration model isn't permanently mycelium-Go-
// tuned: a Python or Rust user can pass --repo <path> + --language
// <lang> and get measurements that update the per-language multiplier
// overrides the v3.4 A3 model now supports.
//
// What's here:
//   - Case + Row + Corpus types — value-only, no I/O.
//   - MyceliumDefaultCorpus() — the v3.4 hard-coded mycelium-self
//     corpus, kept as the default so re-runs against this repo
//     reproduce the calibrated numbers.
//   - Run(...) — the orchestrator. Takes a client + corpus, returns
//     the comparable Row slice. Caller decides how to render.
//
// What's deferred to v4.1+:
//   - BuildAdaptiveCorpus(client) — probe-based corpus that picks
//     symbols/files dynamically from the indexed repo so external
//     repos don't need a hand-tuned corpus to bench against. The
//     v4 B3 ticket calls this out: "Picking representative corpus
//     targets per language is itself a calibration problem." Worth
//     having multi-repo data first before locking in the heuristic.
//   - YAML --corpus-file loader for BYO corpora.
//   - --update-multipliers source-mutating flag (per ticket caveat:
//     "If this feels too magical, drop it from v4 and require manual
//     table edits — it's a convenience, not load-bearing.").
package bench

import (
	"github.com/jdwiederstein/mycelium/internal/ipc"
)

// Case pairs one myco call with its shell-fallback equivalent.
// Exactly one of FallbackCmd or FallbackFile must be set: FallbackCmd
// is run via `bash -c` and the stdout byte length is the measurement;
// FallbackFile is sized via os.Stat (mirrors `wc -c`).
type Case struct {
	Tool         string
	Method       string
	Params       any
	FallbackCmd  string
	FallbackFile string
	Note         string
}

// Corpus is an ordered list of Cases + a stable name so reports can
// distinguish "ran the mycelium-default corpus" from "ran a BYO
// corpus" or "ran the adaptive probe (v4.1+)".
type Corpus struct {
	Name  string
	Cases []Case
}

// Row is one printable line of the bench result. Mirrors the v3.4
// shape so JSON consumers from the old --format json output continue
// to parse correctly after the v4 B3 extraction.
type Row struct {
	Tool          string  `json:"tool"`
	MycoBytes     int64   `json:"myco_bytes"`
	FallbackBytes int64   `json:"fallback_bytes"`
	MeasuredRatio float64 `json:"measured_ratio"`
	ModelRatio    float64 `json:"model_ratio"`
	Drift         float64 `json:"drift"` // |measured - model| / max(model, 0.01)
	Quality       string  `json:"quality"`
	MycoMS        int64   `json:"myco_ms"`
	FallbackMS    int64   `json:"fallback_ms"`
	OK            bool    `json:"ok"`
	Note          string  `json:"note"`
	Err           string  `json:"error,omitempty"`
}

// MyceliumDefaultCorpus is the v3.4 calibrated corpus, hard-coded
// against the mycelium self-index. The targets are picked for stable
// existence across the codebase — bumping or renaming any of these
// in mycelium without updating this list silently zeroes out the
// myco-side measurement and the drift number lies. Cross-checked by
// running the bench periodically; a missing target shows as ERR rows
// in the table output, which is loud enough to catch.
//
// External repos should NOT use this corpus directly — the symbol +
// file targets won't exist. Until v4.1's adaptive corpus lands,
// `myco bench-counterfactual --repo <other>` will surface lots of
// ERRs which IS the friendly fallback (vs. producing garbage numbers).
func MyceliumDefaultCorpus() Corpus {
	return Corpus{
		Name: "mycelium-self",
		Cases: []Case{
			{
				Tool:        "find_symbol",
				Method:      ipc.MethodFindSymbol,
				Params:      ipc.FindSymbolParams{Name: "ComputeSessionCost"},
				FallbackCmd: `grep -rn 'ComputeSessionCost' --include='*.go' .`,
				Note:        "single Go function, ~6 references in the repo",
			},
			{
				Tool:        "get_references",
				Method:      ipc.MethodGetReferences,
				Params:      ipc.GetReferencesParams{Target: "ComputeSessionCost"},
				FallbackCmd: `grep -rn 'ComputeSessionCost' --include='*.go' .`,
				Note:        "callers of ComputeSessionCost",
			},
			{
				Tool:         "read_focused",
				Method:       ipc.MethodReadFocused,
				Params:       ipc.ReadFocusedParams{Path: "internal/telemetry/aggregate.go"},
				FallbackFile: "internal/telemetry/aggregate.go",
				Note:         "no focus → preview path (v4 B1); counterfactual = full Read",
			},
			{
				Tool:         "get_file_outline",
				Method:       ipc.MethodGetFileOutline,
				Params:       ipc.GetFileOutlineParams{Path: "internal/telemetry/aggregate.go"},
				FallbackFile: "internal/telemetry/aggregate.go",
				Note:         "outline vs full Read",
			},
			{
				Tool:         "get_file_summary",
				Method:       ipc.MethodGetFileSummary,
				Params:       ipc.GetFileSummaryParams{Path: "internal/telemetry/aggregate.go"},
				FallbackFile: "internal/telemetry/aggregate.go",
				Note:         "summary vs full Read",
			},
			{
				Tool:        "search_lexical",
				Method:      ipc.MethodSearchLexical,
				Params:      ipc.SearchLexicalParams{Pattern: "telemetry.Record"},
				FallbackCmd: `grep -rn 'telemetry\.Record' --include='*.go' .`,
				Note:        "literal-string search, parity case",
			},
			{
				Tool:        "list_files",
				Method:      ipc.MethodListFiles,
				Params:      ipc.ListFilesParams{NameContains: "telemetry"},
				FallbackCmd: `find . -path ./.git -prune -o -path ./.mycelium -prune -o -name '*telemetry*' -print`,
				Note:        "name-contains filter vs find",
			},
			{
				Tool:        "impact_analysis",
				Method:      ipc.MethodImpactAnalysis,
				Params:      ipc.ImpactAnalysisParams{Target: "ComputeSessionCost"},
				FallbackCmd: `grep -rn 'ComputeSessionCost' --include='*.go' .`,
				Note:        "transitive callers; grep is a lower bound",
			},
			{
				Tool:        "get_neighborhood",
				Method:      ipc.MethodGetNeighborhood,
				Params:      ipc.GetNeighborhoodParams{Target: "ComputeSessionCost", Depth: 1},
				FallbackCmd: `grep -rn 'ComputeSessionCost' --include='*.go' .`,
				Note:        "1-hop graph walk; agent would iterate grep+Read",
			},
		},
	}
}
