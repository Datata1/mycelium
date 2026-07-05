// Package render turns daemon JSON results into compact, LLM-friendly
// text. One exported function per tool; the method→renderer binding
// lives in internal/registry, which is the single tool table.
package render

import (
	"encoding/json"
	"strings"
)

// RawJSON is the fallback for results without a dedicated renderer
// (e.g. read_focused): indented JSON, or the raw bytes if that fails.
func RawJSON(raw json.RawMessage) string {
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// writeHints appends the shared hints block. Non-empty results carry
// hints too (e.g. truncation notices), not just misses.
func writeHints(sb *strings.Builder, hints []string) {
	if len(hints) == 0 {
		return
	}
	sb.WriteString("\nhints:\n  ")
	sb.WriteString(strings.Join(hints, "\n  "))
	sb.WriteByte('\n')
}
