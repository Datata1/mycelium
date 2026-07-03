package doctor

import (
	"testing"

	"github.com/datata1/mycelium/internal/telemetry"
)

// TestEvaluateAdoption_InsufficientData: when sessionCount < MinSessions,
// EvaluateAdoption returns exactly one ModeInsufficientData finding and
// skips the per-mode evaluation. Doctor renders this as "no telemetry
// yet" rather than silently skipping the section.
func TestEvaluateAdoption_InsufficientData(t *testing.T) {
	t.Parallel()
	th := DefaultAdoptionThresholds()
	myco := []telemetry.Summary{{Tool: "find_symbol", Count: 100}}
	got := EvaluateAdoption(myco, nil, th.MinSessions-1, th)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (insufficient-data short-circuit)", len(got))
	}
	if got[0].Mode != ModeInsufficientData {
		t.Errorf("Mode = %q, want %q", got[0].Mode, ModeInsufficientData)
	}
	if got[0].Level != AdoptionLevelInfo {
		t.Errorf("Level = %q, want %q (info, not warn)", got[0].Level, AdoptionLevelInfo)
	}
}

// TestEvaluateAdoption_SearchLexicalDominanceWarn pins the
// search_lexical-only failure mode at the boundary: ratio just over the
// warn threshold fires WARN; just under fires OK.
func TestEvaluateAdoption_SearchLexicalDominanceWarn(t *testing.T) {
	t.Parallel()
	th := DefaultAdoptionThresholds() // SearchLexicalDominanceWarn = 0.70

	// 80% search_lexical → WARN.
	myco := []telemetry.Summary{
		{Tool: "search_lexical", Count: 80},
		{Tool: "find_symbol", Count: 20},
	}
	got := EvaluateAdoption(myco, nil, th.MinSessions, th)
	finding := findMode(t, got, ModeSearchLexicalOnly)
	if finding.Level != AdoptionLevelWarn {
		t.Errorf("80%% lexical: Level = %q, want warn", finding.Level)
	}
	if finding.Hint == "" {
		t.Error("warn finding should carry a Hint pointing at the fix")
	}

	// 50% search_lexical → OK.
	myco = []telemetry.Summary{
		{Tool: "search_lexical", Count: 50},
		{Tool: "find_symbol", Count: 50},
	}
	got = EvaluateAdoption(myco, nil, th.MinSessions, th)
	finding = findMode(t, got, ModeSearchLexicalOnly)
	if finding.Level != AdoptionLevelOK {
		t.Errorf("50%% lexical: Level = %q, want ok", finding.Level)
	}
}

// TestEvaluateAdoption_ReadFocusedUnderUsed: when read_focused is a
// small fraction of file reads, fire WARN. Excludes the case where
// neither tool was used (no opinion possible).
func TestEvaluateAdoption_ReadFocusedUnderUsed(t *testing.T) {
	t.Parallel()
	th := DefaultAdoptionThresholds() // ReadFocusedShareWarn = 0.15

	// 5% read_focused (1 of 20 reads) → WARN.
	myco := []telemetry.Summary{{Tool: "read_focused", Count: 1}}
	fallback := []telemetry.ExternalSummary{{Tool: "Read", Count: 19}}
	got := EvaluateAdoption(myco, fallback, th.MinSessions, th)
	finding := findMode(t, got, ModeReadFocusedUnderUsed)
	if finding.Level != AdoptionLevelWarn {
		t.Errorf("5%% read_focused: Level = %q, want warn", finding.Level)
	}

	// 50% read_focused (5 of 10 reads) → OK.
	myco = []telemetry.Summary{{Tool: "read_focused", Count: 5}}
	fallback = []telemetry.ExternalSummary{{Tool: "Read", Count: 5}}
	got = EvaluateAdoption(myco, fallback, th.MinSessions, th)
	finding = findMode(t, got, ModeReadFocusedUnderUsed)
	if finding.Level != AdoptionLevelOK {
		t.Errorf("50%% read_focused: Level = %q, want ok", finding.Level)
	}

	// Zero reads of either kind → no finding (denominator zero, no opinion).
	got = EvaluateAdoption(nil, nil, th.MinSessions, th)
	for _, f := range got {
		if f.Mode == ModeReadFocusedUnderUsed {
			t.Errorf("expected no read_focused finding when neither was used; got %+v", f)
		}
	}
}

// TestEvaluateAdoption_GrepOverMyco: low myco/grep ratio fires WARN.
// Grep-free sessions skip the finding entirely.
func TestEvaluateAdoption_GrepOverMyco(t *testing.T) {
	t.Parallel()
	th := DefaultAdoptionThresholds() // MycoVsGrepRatioWarn = 1.5

	// 5 myco vs 10 grep → ratio 0.5 → WARN.
	myco := []telemetry.Summary{{Tool: "find_symbol", Count: 5}}
	fallback := []telemetry.ExternalSummary{{Tool: "Bash/grep", Count: 10}}
	got := EvaluateAdoption(myco, fallback, th.MinSessions, th)
	finding := findMode(t, got, ModeGrepOverMyco)
	if finding.Level != AdoptionLevelWarn {
		t.Errorf("ratio 0.5: Level = %q, want warn", finding.Level)
	}

	// 30 myco vs 10 grep → ratio 3.0 → OK.
	myco = []telemetry.Summary{{Tool: "find_symbol", Count: 30}}
	got = EvaluateAdoption(myco, fallback, th.MinSessions, th)
	finding = findMode(t, got, ModeGrepOverMyco)
	if finding.Level != AdoptionLevelOK {
		t.Errorf("ratio 3.0: Level = %q, want ok", finding.Level)
	}

	// Grep-free session → no finding (the ideal case shouldn't surface noise).
	myco = []telemetry.Summary{{Tool: "find_symbol", Count: 30}}
	got = EvaluateAdoption(myco, nil, th.MinSessions, th)
	for _, f := range got {
		if f.Mode == ModeGrepOverMyco {
			t.Errorf("expected no grep_over_myco finding when grep was unused; got %+v", f)
		}
	}
}

// TestEvaluateAdoption_AlternateGrepTools: rg / ripgrep should count as
// the same fallback class — agents using rg shouldn't escape the warn
// just by virtue of using a different binary.
func TestEvaluateAdoption_AlternateGrepTools(t *testing.T) {
	t.Parallel()
	th := DefaultAdoptionThresholds()
	myco := []telemetry.Summary{{Tool: "find_symbol", Count: 5}}
	fallback := []telemetry.ExternalSummary{
		{Tool: "Bash/rg", Count: 5},
		{Tool: "Bash/ripgrep", Count: 5},
	}
	got := EvaluateAdoption(myco, fallback, th.MinSessions, th)
	finding := findMode(t, got, ModeGrepOverMyco)
	if finding.Level != AdoptionLevelWarn {
		t.Errorf("rg/ripgrep should aggregate as grep; Level = %q, want warn", finding.Level)
	}
	if got, want := finding.Detail["bash_grep_calls"], 10; got != want {
		t.Errorf("bash_grep_calls = %v, want %v (rg + ripgrep summed)", got, want)
	}
}

// TestEvaluateAdoption_AllRoll_AllOK: representative healthy session —
// every mode within band, no warns. Sanity check that the OK path
// doesn't erroneously fire warns.
func TestEvaluateAdoption_AllRoll_AllOK(t *testing.T) {
	t.Parallel()
	th := DefaultAdoptionThresholds()
	myco := []telemetry.Summary{
		{Tool: "find_symbol", Count: 30},
		{Tool: "get_references", Count: 15},
		{Tool: "search_lexical", Count: 10}, // 18% — within band
		{Tool: "read_focused", Count: 8},
	}
	fallback := []telemetry.ExternalSummary{
		{Tool: "Read", Count: 5},      // read_focused share = 8/13 = 62%
		{Tool: "Bash/grep", Count: 2}, // ratio 63/2 = 31.5
	}
	got := EvaluateAdoption(myco, fallback, th.MinSessions, th)
	for _, f := range got {
		if f.Level == AdoptionLevelWarn {
			t.Errorf("healthy session should produce no warns; got %+v", f)
		}
	}
}

// findMode is a test helper that fails the test when the requested mode
// isn't in the findings list. Returns the matched finding.
func findMode(t *testing.T, findings []AdoptionFinding, mode AdoptionFindingMode) AdoptionFinding {
	t.Helper()
	for _, f := range findings {
		if f.Mode == mode {
			return f
		}
	}
	t.Fatalf("expected finding for mode %q; got %+v", mode, findings)
	return AdoptionFinding{}
}
