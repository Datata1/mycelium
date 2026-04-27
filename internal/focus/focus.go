// Package focus implements the deterministic, lexical focus filter used
// by v2.4's Pillar I (focused reads). It is intentionally separate from
// internal/query so the same scoring works for symbols pulled from SQL,
// outlines, and graph traversals — and so it has no DB dependency.
//
// The mechanism is the SWE-Pruner pattern (focus-aware, line-level,
// backward-compatible) but explicitly NOT the SWE-Pruner mechanism — no
// neural model. We trade some recall for a single static binary.
package focus

import (
	"strings"
	"unicode"
)

// Candidate is the set of fields a focus query is scored against. All
// fields are optional; missing fields contribute zero to the score.
type Candidate struct {
	Name       string
	Qualified  string
	Docstring  string
	RefTargets []string
}

// Score buckets — tuned so an exact-name hit always outranks a docstring
// hit, and a docstring hit always outranks a peripheral ref-target match.
const (
	ScoreNameExact     = 3.0
	ScoreNameSubstring = 2.0
	ScoreDocSubstring  = 1.0
	ScoreRefSubstring  = 0.5
)

// stopwords drop tokens that carry no signal in code search. Kept short
// on purpose — false negatives from over-aggressive stopwording would
// hide the focus query's intent.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true,
	"of": true, "to": true, "for": true,
	"and": true, "or": true, "in": true,
	"on": true, "is": true, "are": true,
	"this": true, "that": true,
}

// Tokenize lowercases the input, splits on non-alphanumeric runes, and
// drops stopwords + empty fragments. Returned tokens are de-duplicated
// preserving first-seen order (so a focus of "auth login auth" yields
// {"auth","login"} not three lookups).
func Tokenize(focus string) []string {
	if focus == "" {
		return nil
	}
	fields := strings.FieldsFunc(strings.ToLower(focus), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var out []string
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f == "" || stopwords[f] {
			continue
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// Match scores a candidate against a focus string. Returns score and a
// bool indicating "this is a relevant hit" (score > 0). An empty focus
// scores 0 with ok=true so callers using Match as a passthrough filter
// keep all candidates.
func Match(focus string, c Candidate) (float64, bool) {
	tokens := Tokenize(focus)
	if len(tokens) == 0 {
		return 0, true
	}
	return MatchTokens(tokens, c)
}

// MatchTokens is the hot-path variant that lets callers tokenize once
// across many candidates (e.g. filtering an outline list).
func MatchTokens(tokens []string, c Candidate) (float64, bool) {
	if len(tokens) == 0 {
		return 0, true
	}
	name := strings.ToLower(c.Name)
	qualified := strings.ToLower(c.Qualified)
	doc := strings.ToLower(c.Docstring)

	// Lower-cased ref-target slice; allocated once per candidate.
	var refs []string
	if len(c.RefTargets) > 0 {
		refs = make([]string, len(c.RefTargets))
		for i, r := range c.RefTargets {
			refs[i] = strings.ToLower(r)
		}
	}

	var score float64
	for _, tok := range tokens {
		switch {
		case name == tok:
			score += ScoreNameExact
		case name != "" && strings.Contains(name, tok),
			qualified != "" && strings.Contains(qualified, tok):
			score += ScoreNameSubstring
		case doc != "" && strings.Contains(doc, tok):
			score += ScoreDocSubstring
		default:
			for _, r := range refs {
				if strings.Contains(r, tok) {
					score += ScoreRefSubstring
					break
				}
			}
		}
	}
	return score, score > 0
}
