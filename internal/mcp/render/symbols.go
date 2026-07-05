package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

func FindSymbol(raw json.RawMessage) string {
	var r ipc.FindSymbolResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return RawJSON(raw)
	}
	if len(r.Matches) == 0 {
		if len(r.Hints) > 0 {
			return "no matches\nhints:\n  " + strings.Join(r.Hints, "\n  ")
		}
		return "no matches"
	}
	var sb strings.Builder
	for _, m := range r.Matches {
		loc := fmt.Sprintf("%s:%d-%d", m.Path, m.StartLine, m.EndLine)
		fmt.Fprintf(&sb, "%-50s  %-10s  %s\n", m.Qualified, m.Kind, loc)
		if m.Signature != "" {
			fmt.Fprintf(&sb, "  %s\n", m.Signature)
		}
		if m.Docstring != "" {
			first := strings.SplitN(m.Docstring, "\n", 2)[0]
			fmt.Fprintf(&sb, "  %s\n", first)
		}
	}
	writeHints(&sb, r.Hints)
	m := r.Matches[0]
	sb.WriteString(nextLine(
		fmt.Sprintf("callers: get_references(%q)", m.Qualified),
		fmt.Sprintf("graph: get_neighborhood(%q)", m.Qualified),
		fmt.Sprintf("read: read_focused(%q, focus=%q)", m.Path, m.Name),
	))
	return strings.TrimRight(sb.String(), "\n")
}

func FileOutline(raw json.RawMessage) string {
	var items []ipc.FileOutlineItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return RawJSON(raw)
	}
	if len(items) == 0 {
		return "no symbols"
	}
	var sb strings.Builder
	writeOutlineItems(&sb, items, 0)
	return strings.TrimRight(sb.String(), "\n")
}

func writeOutlineItems(sb *strings.Builder, items []ipc.FileOutlineItem, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, it := range items {
		fmt.Fprintf(sb, "%s%-10s %-40s :%d\n", indent, it.Kind, it.Name, it.StartLine)
		if len(it.Children) > 0 {
			writeOutlineItems(sb, it.Children, depth+1)
		}
	}
}

func FileSummary(raw json.RawMessage) string {
	var s ipc.FileSummary
	if err := json.Unmarshal(raw, &s); err != nil {
		return RawJSON(raw)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s  %s  %d loc  %d symbols\n", s.Path, s.Language, s.LOC, s.SymbolCount)
	if len(s.ByKind) > 0 {
		parts := make([]string, 0, len(s.ByKind))
		for k, v := range s.ByKind {
			parts = append(parts, fmt.Sprintf("%s: %d", k, v))
		}
		fmt.Fprintf(&sb, "by_kind: %s\n", strings.Join(parts, "  "))
	}
	if len(s.Exports) > 0 {
		sb.WriteString("exports:\n")
		for _, e := range s.Exports {
			fmt.Fprintf(&sb, "  %-10s %s  :%d\n", e.Kind, e.Qualified, e.StartLine)
		}
	}
	if len(s.Imports) > 0 {
		sb.WriteString("imports:\n")
		for _, imp := range s.Imports {
			fmt.Fprintf(&sb, "  %s\n", imp)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
