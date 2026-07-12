package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

// maxDanglersShown bounds the per-symbol call-site list; the full set
// is always in the JSON result for programmatic consumers.
const maxDanglersShown = 5

// Verify renders a verify_changes report in the doctor style: one line
// per check, then the removed symbols with the call sites that still
// reference them.
func Verify(raw json.RawMessage) string {
	var r ipc.VerifyReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return RawJSON(raw)
	}
	var sb strings.Builder
	for _, c := range r.Checks {
		mark := "✓"
		switch c.Level {
		case "warn":
			mark = "!"
		case "fail":
			mark = "✗"
		}
		fmt.Fprintf(&sb, "%s %s — %s\n", mark, c.Name, c.Message)
		if ex, ok := c.Detail["examples"].([]any); ok {
			for _, e := range ex {
				fmt.Fprintf(&sb, "    %v\n", e)
			}
		}
	}
	for _, rm := range r.Removed {
		if len(rm.Danglers) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "  removed %s [%s] (was %s):\n", rm.Qualified, rm.Kind, rm.OldPath)
		for i, d := range rm.Danglers {
			if i == maxDanglersShown {
				fmt.Fprintf(&sb, "    … and %d more call site(s)\n", len(rm.Danglers)-maxDanglersShown)
				break
			}
			confidence := "exact"
			if !d.Exact {
				confidence = "short-name match"
			}
			src := d.SrcSymbol
			if src == "" {
				src = "top level"
			}
			fmt.Fprintf(&sb, "    %s:%d  [%s] from %s (%s)\n", d.Path, d.Line, d.Kind, src, confidence)
		}
	}
	fmt.Fprintf(&sb, "summary: %d pass · %d warn · %d fail", r.Summary.Pass, r.Summary.Warn, r.Summary.Fail)
	if r.Summary.Fail == 0 && r.Summary.Warn == 0 && r.ChangedFiles > 0 {
		sb.WriteString("  — structurally clean; run the selected tests next (select_tests)")
	}
	sb.WriteByte('\n')
	return sb.String()
}

// SelectTests renders the test selection one path per line so shell
// loops can pipe it straight into a test runner.
func SelectTests(raw json.RawMessage) string {
	var r ipc.SelectTestsResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return RawJSON(raw)
	}
	var sb strings.Builder
	if len(r.TestFiles) == 0 {
		sb.WriteString("no test files selected\n")
	}
	for _, tf := range r.TestFiles {
		fmt.Fprintf(&sb, "%s", tf.Path)
		if tf.Project != "" {
			fmt.Fprintf(&sb, "  (project: %s)", tf.Project)
		}
		fmt.Fprintf(&sb, "  d=%d\n", tf.Distance)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&sb, "note: %s\n", n)
	}
	fmt.Fprintf(&sb, "%d test file(s) for %d changed file(s) (%d seed symbols)\n",
		len(r.TestFiles), r.ChangedFiles, r.Seeds)
	return sb.String()
}
