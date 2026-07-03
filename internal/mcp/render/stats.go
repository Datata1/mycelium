package render

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

func renderStats(raw json.RawMessage) string {
	var s ipc.Stats
	if err := json.Unmarshal(raw, &s); err != nil {
		return fallback(raw)
	}
	var sb strings.Builder

	dbMB := float64(s.DBSizeBytes) / (1024 * 1024)
	fmt.Fprintf(&sb, "files: %d  symbols: %d  refs: %d (%d resolved, %d unresolved)\n",
		s.Files, s.Symbols, s.Refs, s.Resolved, s.RefsTrulyUnresolved)
	if len(s.ByLang) > 0 {
		langs := make([]string, 0, len(s.ByLang))
		for l := range s.ByLang {
			langs = append(langs, l)
		}
		sort.Strings(langs)
		parts := make([]string, 0, len(langs))
		for _, l := range langs {
			parts = append(parts, fmt.Sprintf("%s: %d", l, s.ByLang[l]))
		}
		fmt.Fprintf(&sb, "by_language: %s\n", strings.Join(parts, "  "))
	}
	if !s.LastScan.IsZero() {
		fmt.Fprintf(&sb, "last_scan: %s  db: %.1f MB\n", s.LastScan.Format("2006-01-02T15:04:05Z07:00"), dbMB)
	} else {
		fmt.Fprintf(&sb, "db: %.1f MB\n", dbMB)
	}
	if len(s.ConfiguredProjects) > 0 {
		sb.WriteString("projects:\n")
		for _, p := range s.ConfiguredProjects {
			fmt.Fprintf(&sb, "  %-30s  %d files  root: %s\n", p.Name, p.FileCount, p.Root)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
