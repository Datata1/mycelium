package render

import (
	"fmt"
	"regexp"
	"strings"
)

// Follow-up nudges: selected tool results end with a single "next:" line
// suggesting the natural follow-up calls, derived deterministically from
// the FIRST hit only. The static tool descriptions carry this guidance
// too, but agents act on what's in front of them — a result that names
// the next tool at the moment of use is what moves usage beyond
// find_symbol + search_lexical. Budget: one line, ~25–40 tokens; only on
// the entry-point tools (find_symbol, search_lexical, get_references) —
// nudging every tool would devalue the signal.

// nextLine joins follow-up suggestions into the single trailing line.
func nextLine(parts ...string) string {
	return "next: " + strings.Join(parts, " · ")
}

// definitionPatterns match source lines that *define* a symbol — the
// shapes a grep-style search lands on when the agent is actually doing
// symbol navigation. Ordered; first match wins. Each pattern's first
// capture group is the symbol name.
var definitionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*\(`),                                            // Go func / method
	regexp.MustCompile(`^type\s+([A-Za-z_]\w*)\s+(?:struct|interface|func)\b`),                                    // Go type
	regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`),                                              // Python
	regexp.MustCompile(`^\s*class\s+([A-Za-z_]\w*)`),                                                              // Python / TS class
	regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:abstract\s+)?(?:class|interface|enum)\s+([A-Za-z_]\w*)`), // TS
	regexp.MustCompile(`^(?:export\s+)?type\s+([A-Za-z_]\w*)\s*=`),                                                // TS type alias
	regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_]\w*)\s*\(`),                              // TS/JS function
}

// detectDefinition reports the symbol name when a snippet line looks
// like a symbol definition.
func detectDefinition(snippet string) (string, bool) {
	for _, re := range definitionPatterns {
		if m := re.FindStringSubmatch(snippet); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// lexicalDefinitionNote scans hit snippets (in order) and, for the first
// definition-shaped one, returns the advisory note steering the agent
// from grep-mode back to the code graph.
func lexicalDefinitionNote(snippets []string) (string, bool) {
	for _, s := range snippets {
		if name, ok := detectDefinition(strings.TrimSpace(s)); ok {
			return fmt.Sprintf(
				"note: %q looks like a symbol definition — find_symbol(%q) gives the definition; get_references(%q) lists callers.",
				name, name, name), true
		}
	}
	return "", false
}
