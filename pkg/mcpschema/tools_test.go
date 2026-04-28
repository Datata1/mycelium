package mcpschema

import (
	"strings"
	"testing"
)

// TestTools_DescriptionsHaveReachForGuidance is the v3.1 A3 contract.
// Every tool's Description must (1) be non-empty, (2) consist of at
// least two sentences, and (3) include a "reach-for-me-when" cue —
// one of "reach", "use", "instead", or "prefer". Wording is allowed
// to drift; the structural shape is what we lock in.
func TestTools_DescriptionsHaveReachForGuidance(t *testing.T) {
	cues := []string{"reach", "use ", "use this", "use it", "instead", "prefer", "before", "after"}
	for _, tool := range Tools() {
		desc := tool.Description
		if desc == "" {
			t.Errorf("%s: empty description", tool.Name)
			continue
		}
		if sentences := countSentences(desc); sentences < 2 {
			t.Errorf("%s: expected >= 2 sentences, got %d (%q)", tool.Name, sentences, desc)
		}
		lower := strings.ToLower(desc)
		hit := false
		for _, cue := range cues {
			if strings.Contains(lower, cue) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("%s: description missing reach-for-me cue (any of %v): %q",
				tool.Name, cues, desc)
		}
	}
}

// TestTools_HighPriorityToolsContrastWithWrongTool covers the four
// tools the field test showed agents under-using or mis-using. Their
// descriptions must explicitly contrast with the tool the agent
// reflexively reached for instead. This is the load-bearing change of
// v3.1 A3 — generic "use this when..." prose isn't enough.
func TestTools_HighPriorityToolsContrastWithWrongTool(t *testing.T) {
	contrasts := map[string]string{
		"find_symbol":      "string search", // not search_lexical literally; phrasing is more general
		"read_focused":     "general-purpose file reader",
		"get_references":   "string-search",
		"get_neighborhood": "find_symbol",
		"search_lexical":   "find_symbol", // the must-not-use-for path
	}
	tools := map[string]string{}
	for _, tool := range Tools() {
		tools[tool.Name] = tool.Description
	}
	for name, mustContain := range contrasts {
		desc, ok := tools[name]
		if !ok {
			t.Errorf("tool %s missing from Tools()", name)
			continue
		}
		if !strings.Contains(strings.ToLower(desc), strings.ToLower(mustContain)) {
			t.Errorf("%s: description must contrast with %q; got: %q", name, mustContain, desc)
		}
	}
}

// countSentences is a deliberately crude split — counts full stops
// outside obvious abbreviations. Good enough for an English-prose
// docstring.
func countSentences(s string) int {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\n') {
			count++
		}
	}
	return count
}
