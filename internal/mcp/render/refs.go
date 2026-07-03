package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

func References(raw json.RawMessage) string {
	var hits []ipc.ReferenceHit
	if err := json.Unmarshal(raw, &hits); err != nil {
		return RawJSON(raw)
	}
	if len(hits) == 0 {
		return "no references"
	}
	var sb strings.Builder
	for _, h := range hits {
		caller := h.SrcSymbolName
		if caller == "" {
			caller = "-"
		}
		fmt.Fprintf(&sb, "%-40s  %s:%d  %s\n", caller, h.SrcPath, h.SrcLine, h.Kind)
	}
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

	// Split edges into callers (in) and callees (out)
	var in, out []ipc.NeighborEdge
	for _, e := range n.Edges {
		switch e.Direction {
		case "in":
			in = append(in, e)
		default:
			out = append(out, e)
		}
	}

	nodeByID := make(map[int64]ipc.NeighborNode, len(n.Nodes))
	for _, nd := range n.Nodes {
		nodeByID[nd.ID] = nd
	}

	if len(out) > 0 {
		sb.WriteString("\ncallees (out):\n")
		for _, e := range out {
			nd := nodeByID[e.ToID]
			fmt.Fprintf(&sb, "  %-50s  %s:%d\n", nd.Qualified, nd.Path, nd.StartLine)
		}
	}
	if len(in) > 0 {
		sb.WriteString("\ncallers (in):\n")
		for _, e := range in {
			nd := nodeByID[e.FromID]
			fmt.Fprintf(&sb, "  %-50s  %s:%d\n", nd.Qualified, nd.Path, nd.StartLine)
		}
	}
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
		sb.WriteString("no callers found")
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
		sb.WriteString("no path found")
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
