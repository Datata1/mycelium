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
		"read_focused":     {4.0, EstimateQualityHigh},
		"get_file_outline": {2.5, EstimateQualityHigh},
		"get_file_summary": {3.0, EstimateQualityHigh},
		"search_lexical":   {1.0, EstimateQualityHigh},

		// Medium-quality entries: model accepts the agent would re-try
		// or qualify the search; the multiplier averages the cases.
		"find_symbol":       {0.8, EstimateQualityMedium},
		"get_references":    {1.8, EstimateQualityMedium},
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

// TestCounterfactualModel_PerLanguageOverride pins the v4 B3 per-language
// override mechanism: when an entry has a `perLang` map, looking up the
// multiplier with a matching language returns the override; an
// unknown language falls back to the default; empty language always
// uses the default. Also asserts that the per-language overrides
// table is currently EMPTY for every tool — v4 B3 wires the
// framework but doesn't populate data; F1 (Python/Django) and F2
// (Rust/Axum) field tests fill it in.
//
// To intentionally add a per-language override:
//  1. Run `myco bench-counterfactual --repo <repo> --language <lang>`
//     against the target repo and capture the measured ratio.
//  2. Add the override to the relevant entry's `perLang` map.
//  3. Update this test's `wantOverrides` map in the same commit so the
//     pin catches accidental edits.
func TestCounterfactualModel_PerLanguageOverride(t *testing.T) {
	t.Parallel()

	// v4 B3: no per-language overrides are populated yet. F1/F2 will
	// populate. When they do, list them here as
	// `{tool: {language: multiplier}}`.
	wantOverrides := map[string]map[string]float64{}

	// Reverse check: any per-language override added without updating
	// this test should fail.
	for tool, entry := range counterfactualModel {
		if entry.perLang == nil {
			continue
		}
		want, pinned := wantOverrides[tool]
		if !pinned {
			t.Errorf("%s: has perLang overrides but not pinned in wantOverrides", tool)
			continue
		}
		for lang, mul := range entry.perLang {
			if want[lang] != mul {
				t.Errorf("%s perLang[%q] = %v, want %v", tool, lang, mul, want[lang])
			}
		}
		for lang := range want {
			if _, ok := entry.perLang[lang]; !ok {
				t.Errorf("%s: pinned override perLang[%q] missing from model", tool, lang)
			}
		}
	}

	// Forward semantics: language="" or unknown returns the default
	// multiplier; populated overrides win. Use a tool we know has a
	// non-zero default and inject a fake override at runtime.
	const tool = "find_symbol"
	mul, _, _ := CounterfactualMultiplierFor(tool, "")
	if mul != 0.8 {
		t.Errorf("%s default multiplier = %v, want 0.8", tool, mul)
	}
	mul, _, _ = CounterfactualMultiplierFor(tool, "nonexistent-lang")
	if mul != 0.8 {
		t.Errorf("%s unknown-lang multiplier = %v, want 0.8 (default fallback)", tool, mul)
	}

	// Inject a runtime override and assert it wins. Restore after to
	// keep the model clean for other tests.
	orig := counterfactualModel[tool]
	defer func() { counterfactualModel[tool] = orig }()
	counterfactualModel[tool] = counterfactualEntry{
		multiplier: orig.multiplier,
		quality:    orig.quality,
		perLang:    map[string]float64{"python": 0.5},
	}
	mul, _, _ = CounterfactualMultiplierFor(tool, "python")
	if mul != 0.5 {
		t.Errorf("%s python override = %v, want 0.5", tool, mul)
	}
	mul, _, _ = CounterfactualMultiplierFor(tool, "go")
	if mul != 0.8 {
		t.Errorf("%s go (no override) = %v, want 0.8 default", tool, mul)
	}

	// EstimateCounterfactualFor: same logic, byte-multiplied.
	got := EstimateCounterfactualFor(tool, 10_000, "python")
	if got.Bytes != 5_000 {
		t.Errorf("EstimateCounterfactualFor python = %d, want 5000 (10000 × 0.5)", got.Bytes)
	}
	got = EstimateCounterfactualFor(tool, 10_000, "")
	if got.Bytes != 8_000 {
		t.Errorf("EstimateCounterfactualFor empty-lang = %d, want 8000 (10000 × 0.8 default)", got.Bytes)
	}
}
