package telemetry

// counterfactual.go answers v3.4 A3's question: how many bytes would the
// equivalent grep / Read / find operation have cost if the agent didn't
// have myco available?
//
// **Modelled, not measured.** A live counterfactual (actually running
// the fallback during each myco call) would add seconds of latency per
// call and re-traverse the filesystem on every search — destroying the
// v2.4 speed promise. We trade accuracy for a per-call estimate that's
// good enough to track adoption-cost trends across many sessions.
//
// The model is per-tool byte-multipliers applied to the actual
// `OutputBytes` of each myco call. The constants are calibrated
// heuristics — `myco bench-counterfactual` (v3.4 A3 follow-up) is the
// harness that validates them against the self-index and flags drift.
//
// Honest caveats:
//
//  - For tools where the agent would have iterated many fallback calls
//    (get_neighborhood, impact_analysis), the multipliers under-count
//    the cumulative cost. We rate these as `EstimateQualityLow`.
//
//  - For `read_focused`, the multiplier assumes the agent would have
//    fallen back to a full `Read` of the same file. The v2.4 byte
//    reduction measurements (30-80% saved) anchor the constant.
//
//  - For `stats` / `ping`, no fallback equivalent exists; counterfactual
//    is 0.

// CounterfactualEstimate is what the equivalent fallback operation for
// a single myco call would have produced. Quality is "high" when the
// model is well-understood (read_focused → full file size),
// "medium" for symbol-search tools, "low" for graph-walks where
// the agent would have iterated many fallbacks.
type CounterfactualEstimate struct {
	Bytes   int64
	Quality EstimateQuality
}

// EstimateQuality grades how much trust to put in a counterfactual.
// Aggregators can downweight low-quality estimates or surface the
// quality mix in reports.
type EstimateQuality string

const (
	EstimateQualityHigh   EstimateQuality = "high"
	EstimateQualityMedium EstimateQuality = "medium"
	EstimateQualityLow    EstimateQuality = "low"
	EstimateQualityNone   EstimateQuality = "none" // no plausible fallback
)

// counterfactualModel is the per-tool multiplier table. The multiplier
// applies to the myco call's actual `OutputBytes` and returns an
// estimate of what the equivalent fallback (grep / Read / find) would
// have produced.
//
// Ratios are anchored as follows:
//
//   - `read_focused`: **calibrated** to 1.0× high quality. Initial
//     2.0× guess assumed the v2.4 byte-reduction story (30-80% saved
//     when focus is set) generalised to all calls. Bench measured the
//     no-focus pessimistic case on `aggregate.go`: myco returned
//     14 KiB for a 12 KiB file — *heavier* than a plain Read because
//     of the JSON envelope + line markers. This is the G2 adoption-
//     fixed-point signal made measurable: agents that call
//     read_focused without `focus` pay overhead instead of saving. The
//     multiplier now reflects the honest average — closer to parity
//     than savings — so the aggregator stops over-crediting myco for
//     a tool that's net-negative when used wrong.
//
//   - `find_symbol`: a grep call returns a path:line:snippet line per
//     match (~120 B). myco returns structured JSON (~300 B per hit).
//     N hits → grep ~120N, myco ~300N → counterfactual ≈ 0.4× the
//     myco output. But the agent typically narrows the grep with
//     multiple flags and re-tries → effective counterfactual is
//     larger. Set at 0.8× to acknowledge both factors roughly cancel.
//
//   - `search_lexical`: parity. The myco output IS the grep output
//     with metadata. Counterfactual ≈ 1.0×.
//
//   - `get_file_outline`: alternative is full Read. **Calibrated**
//     against `internal/telemetry/aggregate.go` via
//     `myco bench-counterfactual` — outline is ~40% of full file for
//     Go files with extensive doc comments, so counterfactual ≈ 2.5×.
//     Pre-calibration estimate of 10× (assuming aggressive compression)
//     turned out to be wishful thinking on doc-heavy code.
//
//   - `get_file_summary`: alternative is full Read. **Calibrated** to
//     ≈ 3.0× from the same bench run; the pre-calibration 30× guess
//     similarly assumed compression that doesn't hold for the
//     mycelium codebase.
//
//   - `get_references`: grep -rn 'target' produces ~120 B/match.
//     myco's structured output is ~250 B/match. Counterfactual
//     ~0.5× the myco output but iterated grep variants
//     (qualified + short name + textual fallback) inflate the agent's
//     real cost. Set at 1.2× as a compromise.
//
//   - `get_neighborhood`: graph walks need iterated grep + Read for
//     each hop. Hard to model from output bytes alone. 2.5× is a
//     guess; quality = low.
//
//   - `impact_analysis`: similar to get_references but transitively
//     expanded. 1.5× with quality = low.
//
//   - `critical_path`: very hard to fake via grep. We model as 3×
//     with quality = low.
//
//   - `find_document_key`: grep on the document files. ~1.2× with
//     quality = medium (grep often returns more context lines).
//
//   - `list_files`: **calibrated** to 0.2× medium quality. `find` returns
//     bare paths (~30 B/hit); myco returns structured metadata
//     (language, path, line counts) per row — measurably MORE bytes
//     than the equivalent `find -name`. Initial 1.0× guess assumed
//     parity but the bench showed myco is the heavier of the two for
//     this surface. Honest negative-savings signal stays in the
//     aggregate.
//
//   - `search_semantic`: no real grep equivalent. The agent would
//     have done multiple `find_symbol` + Read calls. Model as 2.5×
//     with quality = low.
//
//   - `stats` / `ping`: no fallback. 0× with quality = none.
type counterfactualEntry struct {
	multiplier float64
	quality    EstimateQuality
}

var counterfactualModel = map[string]counterfactualEntry{
	"find_symbol":       {0.8, EstimateQualityMedium},
	"get_references":    {1.2, EstimateQualityMedium},
	"read_focused":      {1.0, EstimateQualityHigh},
	"get_file_outline":  {2.5, EstimateQualityHigh},
	"get_file_summary":  {3.0, EstimateQualityHigh},
	"search_lexical":    {1.0, EstimateQualityHigh},
	"search_semantic":   {2.5, EstimateQualityLow},
	"get_neighborhood":  {2.5, EstimateQualityLow},
	"impact_analysis":   {1.5, EstimateQualityLow},
	"critical_path":     {3.0, EstimateQualityLow},
	"find_document_key": {1.2, EstimateQualityMedium},
	"list_files":        {0.2, EstimateQualityMedium},
	// Tools with no plausible non-myco alternative — counterfactual 0.
	"stats": {0, EstimateQualityNone},
	"ping":  {0, EstimateQualityNone},
}

// EstimateCounterfactual returns the byte cost the equivalent fallback
// operation would have produced, given a myco call's actual output
// byte count. Unknown tool names return {0, EstimateQualityNone} — the
// caller can decide whether that means "no comparison possible" or
// silently skip the row in aggregation.
func EstimateCounterfactual(tool string, outputBytes int64) CounterfactualEstimate {
	entry, ok := counterfactualModel[tool]
	if !ok {
		return CounterfactualEstimate{Quality: EstimateQualityNone}
	}
	if entry.multiplier == 0 {
		return CounterfactualEstimate{Quality: entry.quality}
	}
	return CounterfactualEstimate{
		Bytes:   int64(float64(outputBytes) * entry.multiplier),
		Quality: entry.quality,
	}
}

// CounterfactualMultiplier returns the multiplier for a known tool,
// or 0 + false for unknown tools. Exported so the calibration harness
// (v3.4 A3 follow-up) can compare measured ratios against the model.
func CounterfactualMultiplier(tool string) (float64, EstimateQuality, bool) {
	entry, ok := counterfactualModel[tool]
	if !ok {
		return 0, EstimateQualityNone, false
	}
	return entry.multiplier, entry.quality, true
}
