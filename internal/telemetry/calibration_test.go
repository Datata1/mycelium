package telemetry

import "testing"

// TestCounterfactualModel_PinnedMultipliers freezes the multiplier table
// against accidental drift. The constants live in counterfactual.go and
// were calibrated against the mycelium self-index via
// `myco bench-counterfactual` — bumping them without re-running the
// bench means every recorded session retroactively changes its savings
// number, which would silently invalidate trend lines.
//
// To intentionally update a multiplier:
//  1. Change the value in counterfactual.go.
//  2. Run `myco bench-counterfactual` and capture the measured ratio.
//  3. Update both the multiplier and this test in the same commit, with
//     the bench output pasted into the CHANGELOG entry.
//
// The test fails loudly so the change can't slip through review by
// accident.
func TestCounterfactualModel_PinnedMultipliers(t *testing.T) {
	t.Parallel()
	want := map[string]struct {
		mul     float64
		quality EstimateQuality
	}{
		// High-quality entries: bench measured these directly against the
		// self-index. Drift > a few percent means the corpus changed or
		// the tool's output shape changed.
		"read_focused":     {1.0, EstimateQualityHigh},
		"get_file_outline": {2.5, EstimateQualityHigh},
		"get_file_summary": {3.0, EstimateQualityHigh},
		"search_lexical":   {1.0, EstimateQualityHigh},

		// Medium-quality entries: model accepts the agent would re-try
		// or qualify the search; the multiplier averages the cases.
		"find_symbol":       {0.8, EstimateQualityMedium},
		"get_references":    {1.2, EstimateQualityMedium},
		"list_files":        {0.2, EstimateQualityMedium},
		"find_document_key": {1.2, EstimateQualityMedium},

		// Low-quality entries: graph walks where the agent's true cost
		// is hard to model from output bytes alone. The bench is allowed
		// to drift more than the threshold for these (see bench code).
		"search_semantic":  {2.5, EstimateQualityLow},
		"get_neighborhood": {2.5, EstimateQualityLow},
		"impact_analysis":  {1.5, EstimateQualityLow},
		"critical_path":    {3.0, EstimateQualityLow},

		// No-fallback entries: the agent has no equivalent shell command
		// for these, so counterfactual is 0 by design.
		"stats": {0, EstimateQualityNone},
		"ping":  {0, EstimateQualityNone},
	}

	for tool, w := range want {
		mul, qual, ok := CounterfactualMultiplier(tool)
		if !ok {
			t.Errorf("%s: missing from counterfactualModel", tool)
			continue
		}
		if mul != w.mul {
			t.Errorf("%s: multiplier = %v, want %v (re-run `myco bench-counterfactual` and update both files together)",
				tool, mul, w.mul)
		}
		if qual != w.quality {
			t.Errorf("%s: quality = %q, want %q", tool, qual, w.quality)
		}
	}

	// Reverse check: any tool added to counterfactualModel without
	// updating this test should fail. Catches the "added a new tool,
	// forgot to pin it" case.
	for tool := range counterfactualModel {
		if _, pinned := want[tool]; !pinned {
			t.Errorf("%s: present in counterfactualModel but not pinned in this test (add it to `want`)", tool)
		}
	}
}
