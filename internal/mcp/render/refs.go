package render

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

func References(raw json.RawMessage) string {
	var r ipc.GetReferencesResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return RawJSON(raw)
	}
	hits := r.Matches
	if len(hits) == 0 {
		if len(r.Hints) > 0 {
			return "no references\nhints:\n  " + strings.Join(r.Hints, "\n  ")
		}
		return "no references"
	}
	var sb strings.Builder
	for _, h := range hits {
		caller := h.SrcSymbolName
		if caller == "" {
			caller = "-"
		}
		// resolved = graph-linked; textual = name-match only. The tool
		// description promises this flag — keep the MCP surface honest.
		tag := "textual"
		if h.Resolved {
			tag = "resolved"
		}
		fmt.Fprintf(&sb, "%-40s  %s:%d  [%s/%s] -> %s\n",
			caller, h.SrcPath, h.SrcLine, h.Kind, tag, h.DstName)
	}
	writeHints(&sb, r.Hints)
	h := hits[0]
	sb.WriteString(nextLine(
		fmt.Sprintf("read a call site: read_focused(%q, focus=%q)", h.SrcPath, h.DstName),
		fmt.Sprintf("transitive callers: impact_analysis(%q)", h.DstName),
	))
	return strings.TrimRight(sb.String(), "\n")
}

func Neighborhood(raw json.RawMessage) string {
	var n ipc.Neighborhood
	if err := json.Unmarshal(raw, &n); err != nil {
		return RawJSON(raw)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "seed: %-50s  %-10s  %s:%d\n",
		n.Seed.Qualified, n.Seed.Kind, n.Seed.Path, n.Seed.StartLine)

	nodeByID := make(map[int64]ipc.NeighborNode, len(n.Nodes))
	for _, nd := range n.Nodes {
		nodeByID[nd.ID] = nd
	}

	// One row per reachable node, not per edge: multiple call sites into
	// the same node would otherwise print duplicate lines. Rows carry the
	// node's discovery depth, and depth≥2 rows keep the near endpoint of
	// their first-seen edge so the topology stays readable.
	writeSection := func(title string, farCallsNear bool, far func(ipc.NeighborEdge) (int64, string)) {
		type row struct {
			depth int
			label string
			node  ipc.NeighborNode
		}
		var rows []row
		seen := map[int64]bool{}
		for _, e := range n.Edges {
			farID, near := far(e)
			if farID == 0 || seen[farID] {
				continue
			}
			nd, ok := nodeByID[farID]
			if !ok {
				continue
			}
			seen[farID] = true
			label := nd.Qualified
			if nd.Depth >= 2 && near != "" {
				// Arrow always points caller -> callee.
				if farCallsNear {
					label = nd.Qualified + " -> " + near
				} else {
					label = near + " -> " + nd.Qualified
				}
			}
			rows = append(rows, row{depth: nd.Depth, node: nd, label: label})
		}
		if len(rows) == 0 {
			return
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].depth < rows[j].depth })
		fmt.Fprintf(&sb, "\n%s\n", title)
		for _, r := range rows {
			fmt.Fprintf(&sb, "  [d=%d] %-50s  %s:%d\n", r.depth, r.label, r.node.Path, r.node.StartLine)
		}
	}
	writeSection("callees (out):", false, func(e ipc.NeighborEdge) (int64, string) {
		if e.Direction == "in" {
			return 0, ""
		}
		return e.ToID, e.FromName
	})
	writeSection("callers (in):", true, func(e ipc.NeighborEdge) (int64, string) {
		if e.Direction != "in" {
			return 0, ""
		}
		return e.FromID, e.ToName
	})
	for _, note := range n.Notes {
		fmt.Fprintf(&sb, "\nnote: %s\n", note)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func Impact(raw json.RawMessage) string {
	var imp ipc.Impact
	if err := json.Unmarshal(raw, &imp); err != nil {
		return RawJSON(raw)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "seed: %-50s  %-10s  %s:%d\n",
		imp.Seed.Qualified, imp.Seed.Kind, imp.Seed.Path, imp.Seed.StartLine)
	if len(imp.Hits) == 0 {
		sb.WriteString("no callers found — if unexpected, stats shows this repo's unresolved-refs ratio")
	} else {
		sb.WriteString("\ncallers (transitive):\n")
		for _, h := range imp.Hits {
			fmt.Fprintf(&sb, "  [d=%d] %-50s  %-10s  %s:%d\n",
				h.Distance, h.Qualified, h.Kind, h.Path, h.StartLine)
		}
	}
	for _, note := range imp.Notes {
		fmt.Fprintf(&sb, "\nnote: %s\n", note)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func CriticalPath(raw json.RawMessage) string {
	var r ipc.CriticalPathResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return RawJSON(raw)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "from: %s  %s:%d\n", r.From.Qualified, r.From.Path, r.From.StartLine)
	fmt.Fprintf(&sb, "to:   %s  %s:%d\n", r.To.Qualified, r.To.Path, r.To.StartLine)
	if len(r.Paths) == 0 {
		sb.WriteString("no path found — paths follow *outbound* call edges from 'from'; try swapping from/to, or get_neighborhood on either end")
	} else {
		for i, path := range r.Paths {
			fmt.Fprintf(&sb, "\npath %d:\n", i+1)
			for j, v := range path {
				fmt.Fprintf(&sb, "  %d. %-50s  %s:%d\n", j+1, v.Qualified, v.Path, v.StartLine)
			}
		}
	}
	for _, note := range r.Notes {
		fmt.Fprintf(&sb, "\nnote: %s\n", note)
	}
	return strings.TrimRight(sb.String(), "\n")
}
