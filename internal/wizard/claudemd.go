package wizard

import (
	"os"
	"strings"
)

// primingSnippet is the block appended to CLAUDE.md. It is intentionally
// short — one paragraph of orientation, not a manual. The MCP tool
// descriptions (rewritten in v3.1) carry the per-tool details.
const primingSnippet = `
## mycelium (myco)

myco is a local code knowledge base exposed as MCP tools. Reach for it
**before** ` + "`Bash(grep)`" + ` or ` + "`Read`" + ` for any code navigation task.

Key tools: ` + "`find_symbol`" + ` · ` + "`get_references`" + ` · ` + "`read_focused`" + ` ·
` + "`get_neighborhood`" + ` · ` + "`search_lexical`" + ` · ` + "`impact_analysis`" + `

Check ` + "`myco stats`" + ` (index health) and ` + "`myco doctor`" + ` (quality signals)
when results look wrong. Skills tree at ` + "`.mycelium/skills/`" + ` if compiled.
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
