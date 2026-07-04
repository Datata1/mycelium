package render

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

func Stats(raw json.RawMessage) string {
	var s ipc.Stats
	if err := json.Unmarshal(raw, &s); err != nil {
		return RawJSON(raw)
	}
	if s.Files == 0 {
		return "index is empty — run `myco index` or start `myco daemon`"
	}
	var sb strings.Builder

	dbMB := float64(s.DBSizeBytes) / (1024 * 1024)
	fmt.Fprintf(&sb, "files: %d  symbols: %d  refs: %d (%d resolved, %d unresolved)\n",
		s.Files, s.Symbols, s.Refs, s.Resolved, s.RefsTrulyUnresolved)
	if s.NonImportRefs > 0 {
		fmt.Fprintf(&sb, "unresolved_ratio: %.1f%% (%d/%d non-import refs; %d known-external, %d type-resolved)\n",
			s.UnresolvedRatio()*100, s.RefsTrulyUnresolved, s.NonImportRefs,
			s.RefsExternalKnown, s.RefsTypeResolved)
	}
	if len(s.ByLang) > 0 {
		fmt.Fprintf(&sb, "by_language: %s\n", sortedCounts(s.ByLang))
	}
	if len(s.DocumentsByKind) > 0 {
		fmt.Fprintf(&sb, "documents: %s\n", sortedCounts(s.DocumentsByKind))
	}
	if s.InterfaceImplementsRefs > 0 {
		fmt.Fprintf(&sb, "interface_edges: %d (from %d concrete types)\n",
			s.InterfaceImplementsRefs, s.InterfaceConcreteTypes)
	}
	var scanParts []string
	if !s.LastScan.IsZero() {
		scanParts = append(scanParts, "last_scan: "+s.LastScan.Format("2006-01-02T15:04:05Z07:00"))
	}
	if !s.LastFullScan.IsZero() {
		scanParts = append(scanParts, "last_reconcile: "+s.LastFullScan.Format("2006-01-02T15:04:05Z07:00"))
	}
	scanParts = append(scanParts, fmt.Sprintf("db: %.1f MB", dbMB))
	sb.WriteString(strings.Join(scanParts, "  "))
	sb.WriteByte('\n')
	if len(s.ConfiguredProjects) > 0 {
		sb.WriteString("projects:\n")
		for _, p := range s.ConfiguredProjects {
			fmt.Fprintf(&sb, "  %-30s  %d files  root: %s\n", p.Name, p.FileCount, p.Root)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// sortedCounts renders a count map as "k1: n1  k2: n2" in key order —
// deterministic output for the golden tests.
func sortedCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, m[k]))
	}
	return strings.Join(parts, "  ")
}
