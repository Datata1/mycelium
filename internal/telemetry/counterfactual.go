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
//   - `read_focused`: **re-calibrated** to 4.0× high quality after
//     v4 B1 landed. The pre-B1 measurement was 0.87× (myco heavier
//     than Read) because no-focus calls returned the full file plus
//     envelope. v4 B1 made no-focus calls return outline + first 50
//     lines + hint — bench against the same `aggregate.go` corpus
//     now measures 4.43× (myco 2.8 KiB vs Read 12.2 KiB). Set at
//     4.0× to leave headroom for files in the 100-200 line range
//     where the 50-line cap saves less proportionally. The 2.0×
//     pre-bench guess turned out to be too conservative for the
//     post-B1 shape; the 1.0× post-bench-pre-B1 value reflected the
//     bug, not the model.
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

	// v4 B3: per-language overrides. Keyed by Stats.ByLang's language
	// string ("go", "typescript", "python", "rust", …). When the
	// caller provides a language and an override exists, it wins over
	// `multiplier`. Empty / missing language → use the default.
	//
	// Populated empty in v4 B3: the framework is wired but the data
	// hasn't been gathered yet. F1 (Python/Django) and F2 (Rust/Axum)
	// field tests will populate the relevant entries via
	// `myco bench-counterfactual --language <lang>` against a real repo
	// and the calibration_test.go pinned test will lock the values in.
	perLang map[string]float64
}

var counterfactualModel = map[string]counterfactualEntry{
	"find_symbol":       {multiplier: 0.8, quality: EstimateQualityMedium},
	"get_references":    {multiplier: 1.8, quality: EstimateQualityMedium},
	"read_focused":      {multiplier: 4.0, quality: EstimateQualityHigh},
	"get_file_outline":  {multiplier: 2.5, quality: EstimateQualityHigh},
	"get_file_summary":  {multiplier: 3.0, quality: EstimateQualityHigh},
	"search_lexical":    {multiplier: 1.0, quality: EstimateQualityHigh},
	"search_semantic":   {multiplier: 2.5, quality: EstimateQualityLow},
	"get_neighborhood":  {multiplier: 2.5, quality: EstimateQualityLow},
	"impact_analysis":   {multiplier: 1.5, quality: EstimateQualityLow},
	"critical_path":     {multiplier: 3.0, quality: EstimateQualityLow},
	"find_document_key": {multiplier: 1.2, quality: EstimateQualityMedium},
	"list_files":        {multiplier: 0.2, quality: EstimateQualityMedium},
	// Tools with no plausible non-myco alternative — counterfactual 0.
	"stats": {multiplier: 0, quality: EstimateQualityNone},
	"ping":  {multiplier: 0, quality: EstimateQualityNone},
}

// EstimateCounterfactual returns the byte cost the equivalent fallback
// operation would have produced, given a myco call's actual output
// byte count. Unknown tool names return {0, EstimateQualityNone} — the
// caller can decide whether that means "no comparison possible" or
// silently skip the row in aggregation.
//
// Backward-compat shim around EstimateCounterfactualFor with empty
// language; uses the default multiplier even when per-language
// overrides exist.
func EstimateCounterfactual(tool string, outputBytes int64) CounterfactualEstimate {
	return EstimateCounterfactualFor(tool, outputBytes, "")
}

// EstimateCounterfactualFor is the v4 B3 language-aware variant.
// When `language` matches a per-language override entry on the tool's
// model row, the override multiplier wins over the default. Empty
// language or missing override falls through to the default.
func EstimateCounterfactualFor(tool string, outputBytes int64, language string) CounterfactualEstimate {
	entry, ok := counterfactualModel[tool]
	if !ok {
		return CounterfactualEstimate{Quality: EstimateQualityNone}
	}
	mul := entry.multiplier
	if language != "" {
		if override, ok := entry.perLang[language]; ok {
			mul = override
		}
	}
	if mul == 0 {
		return CounterfactualEstimate{Quality: entry.quality}
	}
	return CounterfactualEstimate{
		Bytes:   int64(float64(outputBytes) * mul),
		Quality: entry.quality,
	}
}

// CounterfactualMultiplier returns the multiplier for a known tool,
// or 0 + false for unknown tools. Exported so the calibration harness
// (v3.4 A3 follow-up) can compare measured ratios against the model.
//
// Backward-compat shim around CounterfactualMultiplierFor — returns
// the default multiplier ignoring per-language overrides.
func CounterfactualMultiplier(tool string) (float64, EstimateQuality, bool) {
	return CounterfactualMultiplierFor(tool, "")
}

// CounterfactualMultiplierFor is the v4 B3 language-aware variant of
// CounterfactualMultiplier. Used by the bench harness to compare
// measured ratios against the language-specific model when one
// exists.
func CounterfactualMultiplierFor(tool, language string) (float64, EstimateQuality, bool) {
	entry, ok := counterfactualModel[tool]
	if !ok {
		return 0, EstimateQualityNone, false
	}
	mul := entry.multiplier
	if language != "" {
		if override, ok := entry.perLang[language]; ok {
			mul = override
		}
	}
	return mul, entry.quality, true
}
