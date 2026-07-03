// adoption.go is v4 B2's adoption-health surface for `myco doctor`.
// EvaluateAdoption takes per-tool summaries (myco MCP calls + fallback
// external tool calls) and returns a list of AdoptionFinding rows that
// flag the three documented `docs/adoption.md` failure modes:
//
//  1. **search_lexical-only.** Agent treats myco as a faster grep and
//     skips the graph navigation (find_symbol / get_references / etc).
//  2. **read_focused under-used.** Agent reaches for the
//     general-purpose Read instead of the indexed read_focused, paying
//     full-file bytes when a focused subset would do.
//  3. **grep over myco.** Agent's Bash/grep call count dwarfs the
//     myco call count — myco isn't being reached for at all.
//
// The fourth catalogued failure mode (`read_focused_without_focus`)
// is **deferred** for v4 B2 because the telemetry log doesn't persist
// per-call params. v4 B1's tool-side fix (Hint + preview) already
// gives agents per-call feedback when they call read_focused without
// focus, so the doctor surface is less urgent. v4.1+ can add a
// params-aware variant once a use case demands it.
//
// EvaluateAdoption is a **pure function** — no DB, no I/O. The doctor
// caller does the file-walking + windowing then hands the summaries
// in. This keeps adoption.go trivially testable and lets future
// callers (HTTP introspection, dashboards) reuse the evaluation
// without depending on the doctor's I/O choices.
package doctor

import (
	"fmt"

	"github.com/datata1/mycelium/internal/telemetry"
)

// AdoptionFindingLevel grades a single adoption check. Mirrors the
// doctor Level vocabulary but stays in its own type so the exit-code
// rule for adoption findings (informational, never gate CI) doesn't
// accidentally bleed into the regular doctor checks.
type AdoptionFindingLevel string

const (
	AdoptionLevelOK   AdoptionFindingLevel = "ok"
	AdoptionLevelWarn AdoptionFindingLevel = "warn"
	// AdoptionLevelInfo is for findings that surface data without
	// scoring it — e.g. "not enough sessions yet to evaluate".
	AdoptionLevelInfo AdoptionFindingLevel = "info"
)

// AdoptionFindingMode is the catalogued failure-mode identifier.
// Stable string so dashboards and tests can pin against it.
type AdoptionFindingMode string

const (
	ModeSearchLexicalOnly    AdoptionFindingMode = "search_lexical_only"
	ModeReadFocusedUnderUsed AdoptionFindingMode = "read_focused_under_used"
	ModeGrepOverMyco         AdoptionFindingMode = "grep_over_myco"
	ModeInsufficientData     AdoptionFindingMode = "insufficient_data"
)

// AdoptionFinding is one row of the adoption-health report. Metric
// is the raw measured ratio; Message is the user-facing one-liner;
// Hint points the user at the fix (CLAUDE.md priming, configuring
// telemetry, etc.).
type AdoptionFinding struct {
	Mode    AdoptionFindingMode  `json:"mode"`
	Level   AdoptionFindingLevel `json:"level"`
	Metric  float64              `json:"metric"`
	Message string               `json:"message"`
	Hint    string               `json:"hint,omitempty"`
	Detail  map[string]any       `json:"detail,omitempty"`
}

// AdoptionThresholds calibrates the warn cutoffs. Defaults are
// anchored against the v3.4 mycelium-self-index 16-session aggregate
// (see tickets/v3.4-non-ts-field-test-findings.md). Future field
// tests on different repos may demonstrate a need to retune; the
// thresholds are exposed via Thresholds so config can override.
type AdoptionThresholds struct {
	// MinSessions: at least this many sessions must be present in the
	// window before adoption findings fire. Below this, EvaluateAdoption
	// returns a single ModeInsufficientData info finding so the doctor
	// can surface "not enough data yet" rather than silently skipping.
	MinSessions int

	// SearchLexicalDominanceWarn: warn when search_lexical accounts for
	// MORE than this fraction of total myco calls (the agent is
	// treating myco as a faster grep). Default 0.70 (70%).
	SearchLexicalDominanceWarn float64

	// ReadFocusedShareWarn: warn when read_focused / (read_focused +
	// Read) is BELOW this fraction (the agent is reaching for the
	// general-purpose Read instead of the indexed reader). Default
	// 0.15 (15%).
	ReadFocusedShareWarn float64

	// MycoVsGrepRatioWarn: warn when myco_calls / Bash/grep_calls is
	// BELOW this ratio (myco isn't being reached for at all relative
	// to the agent's grep reflex). Default 1.5 (myco must be at least
	// 1.5× grep usage to be considered adopted).
	MycoVsGrepRatioWarn float64
}

// DefaultAdoptionThresholds returns the calibrated defaults.
func DefaultAdoptionThresholds() AdoptionThresholds {
	return AdoptionThresholds{
		MinSessions:                3,
		SearchLexicalDominanceWarn: 0.70,
		ReadFocusedShareWarn:       0.15,
		MycoVsGrepRatioWarn:        1.5,
	}
}

// EvaluateAdoption is the pure heart of v4 B2. It takes pre-aggregated
// per-tool summaries (myco-side from telemetry.Aggregate, fallback-side
// from telemetry.SummarizeAllExternal) plus the session count over the
// evaluation window, and returns one AdoptionFinding per failure mode
// the data covers. Modes whose denominator is zero (e.g. zero Read +
// zero read_focused) are silently skipped — the data doesn't say
// anything either way and a noisy "n/a" row would dilute the signal.
//
// `sessionCount` is passed separately because the summaries don't
// carry session-count info — the caller knows it from listing the
// session sidecars or counting distinct `sid` fields in the log.
func EvaluateAdoption(myco []telemetry.Summary, fallback []telemetry.ExternalSummary, sessionCount int, th AdoptionThresholds) []AdoptionFinding {
	if sessionCount < th.MinSessions {
		return []AdoptionFinding{{
			Mode:    ModeInsufficientData,
			Level:   AdoptionLevelInfo,
			Message: fmt.Sprintf("not enough telemetry yet (%d sessions in window, need ≥ %d)", sessionCount, th.MinSessions),
			Hint:    "run more sessions with telemetry enabled — see docs/adoption.md for what gets measured",
			Detail: map[string]any{
				"sessions_in_window": sessionCount,
				"min_sessions":       th.MinSessions,
			},
		}}
	}

	// Build per-tool counts from the myco summaries; skip the
	// synthetic "all" rollup so we don't double-count.
	mycoTotal, lexical, readFocused, structural := 0, 0, 0, 0
	structuralTools := map[string]bool{
		"find_symbol":      true,
		"get_references":   true,
		"get_neighborhood": true,
		"impact_analysis":  true,
		"critical_path":    true,
		"get_definition":   true,
	}
	for _, s := range myco {
		if s.Tool == "all" {
			continue
		}
		mycoTotal += s.Count
		switch s.Tool {
		case "search_lexical":
			lexical = s.Count
		case "read_focused":
			readFocused = s.Count
		}
		if structuralTools[s.Tool] {
			structural += s.Count
		}
	}

	// Pull the fallback-side counts. Read can show as "Read" (no
	// detail) and Bash/grep as "Bash/grep" (with detail joined).
	readFallback, bashGrep := 0, 0
	for _, s := range fallback {
		switch s.Tool {
		case "Read":
			readFallback = s.Count
		case "Bash/grep", "Bash/rg", "Bash/ripgrep":
			bashGrep += s.Count
		}
	}

	var out []AdoptionFinding

	// 1. search_lexical-only pattern — measured against total myco calls.
	if mycoTotal > 0 {
		ratio := float64(lexical) / float64(mycoTotal)
		f := AdoptionFinding{
			Mode:   ModeSearchLexicalOnly,
			Metric: ratio,
			Detail: map[string]any{
				"search_lexical_calls": lexical,
				"total_myco_calls":     mycoTotal,
				"structural_calls":     structural,
			},
		}
		if ratio > th.SearchLexicalDominanceWarn {
			f.Level = AdoptionLevelWarn
			f.Message = fmt.Sprintf("search_lexical = %.0f%% of myco calls (warn above %.0f%%)",
				ratio*100, th.SearchLexicalDominanceWarn*100)
			f.Hint = "agent is using myco as grep — add `prefer find_symbol for identifiers` to CLAUDE.md (see docs/adoption.md §search_lexical-only)"
		} else {
			f.Level = AdoptionLevelOK
			f.Message = fmt.Sprintf("search_lexical = %.0f%% of myco calls (within band)", ratio*100)
		}
		out = append(out, f)
	}

	// 2. read_focused under-used vs general-purpose Read.
	if readFocused+readFallback > 0 {
		ratio := float64(readFocused) / float64(readFocused+readFallback)
		f := AdoptionFinding{
			Mode:   ModeReadFocusedUnderUsed,
			Metric: ratio,
			Detail: map[string]any{
				"read_focused_calls":  readFocused,
				"read_fallback_calls": readFallback,
			},
		}
		if ratio < th.ReadFocusedShareWarn {
			f.Level = AdoptionLevelWarn
			f.Message = fmt.Sprintf("read_focused = %.0f%% of file reads (warn below %.0f%%)",
				ratio*100, th.ReadFocusedShareWarn*100)
			f.Hint = "agent reaches for general-purpose Read instead of read_focused — see docs/adoption.md §read_focused-under-used"
		} else {
			f.Level = AdoptionLevelOK
			f.Message = fmt.Sprintf("read_focused = %.0f%% of file reads (within band)", ratio*100)
		}
		out = append(out, f)
	}

	// 3. myco vs Bash/grep — fires only when there's any grep traffic
	// to compare against (a session with zero greps is the ideal case
	// and shouldn't generate a finding).
	if bashGrep > 0 {
		ratio := float64(mycoTotal) / float64(bashGrep)
		f := AdoptionFinding{
			Mode:   ModeGrepOverMyco,
			Metric: ratio,
			Detail: map[string]any{
				"myco_total_calls": mycoTotal,
				"bash_grep_calls":  bashGrep,
			},
		}
		if ratio < th.MycoVsGrepRatioWarn {
			f.Level = AdoptionLevelWarn
			f.Message = fmt.Sprintf("myco/grep ratio = %.1f (warn below %.1f)",
				ratio, th.MycoVsGrepRatioWarn)
			f.Hint = "agent's grep reflex outpaces myco usage — see docs/adoption.md §grep-over-myco for the CLAUDE.md priming snippet"
		} else {
			f.Level = AdoptionLevelOK
			f.Message = fmt.Sprintf("myco/grep ratio = %.1f (within band)", ratio)
		}
		out = append(out, f)
	}

	return out
}
