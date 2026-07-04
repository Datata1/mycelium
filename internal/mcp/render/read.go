package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

// FocusedRead renders the read_focused result: header line with the
// collapse stats, the content verbatim, then the expanded-symbol map and
// any hint. Replaces the RawJSON fallback — dumping the most token-heavy
// tool's output as an escaped JSON blob taught agents to never call it
// again.
func FocusedRead(raw json.RawMessage) string {
	var fr ipc.FocusedRead
	if err := json.Unmarshal(raw, &fr); err != nil {
		return RawJSON(raw)
	}
	var sb strings.Builder

	focus := "no focus"
	if fr.Focus != "" {
		focus = fmt.Sprintf("focus: %q", fr.Focus)
	}
	fmt.Fprintf(&sb, "%s  (%s)  expanded %d/%d symbols  %s/%s\n",
		fr.Path, focus,
		fr.Stats.ExpandedSymbols, fr.Stats.TotalSymbols,
		formatKB(fr.Stats.ReturnedBytes), formatKB(fr.Stats.OriginalBytes))
	sb.WriteString(fr.Content)

	var footer []string
	if len(fr.Expanded) > 0 {
		parts := make([]string, len(fr.Expanded))
		for i, e := range fr.Expanded {
			parts[i] = fmt.Sprintf("%s :%d-%d", e.Qualified, e.StartLine, e.EndLine)
		}
		footer = append(footer, "expanded: "+strings.Join(parts, " · "))
	}
	if fr.Hint != "" {
		footer = append(footer, "hint: "+fr.Hint)
	}
	if len(footer) > 0 {
		if !strings.HasSuffix(fr.Content, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteString("---\n")
		sb.WriteString(strings.Join(footer, "\n"))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatKB(n int) string {
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}
