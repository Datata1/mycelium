package wizard

import (
	"os"
	"strings"
)

// primingSnippet is the block appended to CLAUDE.md. It is intentionally
// short — one paragraph of orientation plus one explicit anti-pattern. The
// MCP tool descriptions carry the per-tool details; this block handles the
// one failure mode that descriptions alone don't prevent: agents using
// search_lexical as a general-purpose grep.
const primingSnippet = `
## mycelium (myco)

myco is a local code knowledge base exposed as MCP tools. Reach for it
**before** ` + "`Bash(grep)`" + ` or ` + "`Read`" + ` for any code navigation task.

**Navigation:** ` + "`find_symbol`" + ` (definitions) · ` + "`get_references`" + ` (callers) ·
` + "`read_focused`" + ` (read a file with irrelevant symbols collapsed) ·
` + "`get_neighborhood`" + ` (local call graph) · ` + "`impact_analysis`" + ` (what depends on X)

**Rule:** when you have an identifier name, use ` + "`find_symbol`" + ` — not
` + "`search_lexical`" + `. ` + "`search_lexical`" + ` is for literal strings and regex patterns
only (log messages, route paths, magic constants). Using it for symbol names
misses renames, aliases, and qualified forms.
`

// primingMarker is a stable string inside primingSnippet used to detect
// whether the snippet is already present (idempotency).
const primingMarker = "myco is a local code knowledge base exposed as MCP tools"

// AppendPrimingSnippet appends the myco orientation block to the
// CLAUDE.md at path. If the file doesn't exist it is created. If the
// snippet is already present (idempotency check) it does nothing and
// returns (false, nil).
func AppendPrimingSnippet(path string) (wrote bool, err error) {
	existing, _ := os.ReadFile(path)
	if strings.Contains(string(existing), primingMarker) {
		return false, nil
	}
	content := string(existing)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += primingSnippet
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// PrimingSnippet returns the raw snippet text for printing.
func PrimingSnippet() string { return primingSnippet }
