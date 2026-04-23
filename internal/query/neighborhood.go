package query

import (
	"context"
	"fmt"
	"strings"
)

// Direction controls which edges get traversed by GetNeighborhood.
// - "out" = follow refs where this symbol is the source (callees)
// - "in"  = follow refs where this symbol is the destination (callers)
// - "both" = union of both
type Direction string

const (
	DirOut  Direction = "out"
	DirIn   Direction = "in"
	DirBoth Direction = "both"
)

// NeighborEdge is one edge in the returned graph.
type NeighborEdge struct {
	FromID    int64  `json:"from_id"`
	FromName  string `json:"from_name"`
	ToID      int64  `json:"to_id"`
	ToName    string `json:"to_name"`
	Kind      string `json:"kind"` // call | import | type_ref | inherit
	SrcPath   string `json:"src_path,omitempty"`
	SrcLine   int    `json:"src_line,omitempty"`
	Depth     int    `json:"depth"`
	Direction string `json:"direction"` // "out" or "in"
}

// NeighborNode is one vertex in the returned graph.
type NeighborNode struct {
	ID        int64  `json:"id"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	Depth     int    `json:"depth"` // 0 = seed, 1 = direct neighbor, ...
}

// Neighborhood is the result of a traversal.
//
// Notes carries non-fatal messages the caller should surface (e.g. depth
// was silently clamped). Transports (CLI, MCP, HTTP) pass these through
// verbatim so agents can reason about the result quality.
type Neighborhood struct {
	Seed  NeighborNode    `json:"seed"`
	Nodes []NeighborNode  `json:"nodes"`
	Edges []NeighborEdge  `json:"edges"`
	Notes []string        `json:"notes,omitempty"`
}

// MaxNeighborhoodDepth is the hard ceiling a caller-visible note is emitted
// for. See LIMITATIONS.md "get_neighborhood silently caps depth at 5" —
// now "get_neighborhood caps depth at 5, surfaces a note."
const MaxNeighborhoodDepth = 5

// GetNeighborhood returns the local call graph around a symbol, up to the
// given depth. This is the graph query the user was asking about: SQLite
// recursive CTEs on the `refs` table handle it just fine for depth 2–3 at
// 50k-symbol scale. If deeper traversals ever become the primary workload,
// this is the exact function to swap for a dedicated graph backend (Kùzu).
func (r *Reader) GetNeighborhood(ctx context.Context, target, project string, depth int, direction Direction) (Neighborhood, error) {
	// v1.5 workspace-mode note: `project` filters the *seed* lookup only.
	// Once we've found the seed symbol, the recursive CTE walks refs
	// across all projects — cross-project call graphs are exactly the
	// thing workspace-mode users want surfaced. For strict isolation,
	// pass project and the seed will refuse to resolve outside it.
	var result Neighborhood
	requestedDepth := depth
	if depth <= 0 {
		depth = 2
	}
	if depth > MaxNeighborhoodDepth {
		depth = MaxNeighborhoodDepth
		result.Notes = append(result.Notes, fmt.Sprintf(
			"depth clamped from %d to %d (see LIMITATIONS.md#graph-queries)",
			requestedDepth, MaxNeighborhoodDepth,
		))
	}
	if direction == "" {
		direction = DirBoth
	}

	seedID, err := r.resolveSeed(ctx, target, project)
	if err != nil {
		return result, err
	}
	if seedID == 0 {
		return result, fmt.Errorf("symbol not found: %q", target)
	}
	seed, err := r.loadNode(ctx, seedID)
	if err != nil {
		return result, err
	}
	seed.Depth = 0
	result.Seed = seed
	result.Nodes = []NeighborNode{seed}

	visited := map[int64]int{seedID: 0}

	switch direction {
	case DirOut, DirBoth:
		edges, nodes, err := r.traverseOutbound(ctx, seedID, depth, visited)
		if err != nil {
			return result, err
		}
		result.Edges = append(result.Edges, edges...)
		result.Nodes = append(result.Nodes, nodes...)
	}
	switch direction {
	case DirIn, DirBoth:
		edges, nodes, err := r.traverseInbound(ctx, seedID, depth, visited)
		if err != nil {
			return result, err
		}
		result.Edges = append(result.Edges, edges...)
		result.Nodes = append(result.Nodes, nodes...)
	}
	return result, nil
}

// traverseOutbound: follow refs.src_symbol_id -> refs.dst_symbol_id.
// Only resolved refs participate; unresolved textual refs don't form a real
// graph edge.
//
// The recursive CTE emits one row per (from, to, depth). We de-dup
// destinations to avoid exponential blowup on diamond-shaped graphs.
func (r *Reader) traverseOutbound(ctx context.Context, seed int64, depth int, visited map[int64]int) ([]NeighborEdge, []NeighborNode, error) {
	q := `
		WITH RECURSIVE walk(from_id, to_id, kind, src_file_id, line, depth) AS (
		    SELECT r.src_symbol_id, r.dst_symbol_id, r.kind, r.src_file_id, r.line, 1
		    FROM refs r
		    WHERE r.src_symbol_id = ? AND r.dst_symbol_id IS NOT NULL
		  UNION
		    SELECT r.src_symbol_id, r.dst_symbol_id, r.kind, r.src_file_id, r.line, w.depth + 1
		    FROM refs r
		    JOIN walk w ON r.src_symbol_id = w.to_id
		    WHERE r.dst_symbol_id IS NOT NULL AND w.depth < ?
		)
		SELECT w.from_id, sf.qualified, w.to_id, st.qualified, w.kind,
		       f.path, w.line, w.depth
		FROM walk w
		JOIN symbols sf ON sf.id = w.from_id
		JOIN symbols st ON st.id = w.to_id
		LEFT JOIN files f ON f.id = w.src_file_id
		ORDER BY w.depth, st.qualified`

	return r.scanNeighborhood(ctx, q, "out", visited, seed, depth)
}

// traverseInbound: the same query with src/dst swapped.
func (r *Reader) traverseInbound(ctx context.Context, seed int64, depth int, visited map[int64]int) ([]NeighborEdge, []NeighborNode, error) {
	q := `
		WITH RECURSIVE walk(from_id, to_id, kind, src_file_id, line, depth) AS (
		    SELECT r.src_symbol_id, r.dst_symbol_id, r.kind, r.src_file_id, r.line, 1
		    FROM refs r
		    WHERE r.dst_symbol_id = ? AND r.src_symbol_id IS NOT NULL
		  UNION
		    SELECT r.src_symbol_id, r.dst_symbol_id, r.kind, r.src_file_id, r.line, w.depth + 1
		    FROM refs r
		    JOIN walk w ON r.dst_symbol_id = w.from_id
		    WHERE r.src_symbol_id IS NOT NULL AND w.depth < ?
		)
		SELECT w.from_id, sf.qualified, w.to_id, st.qualified, w.kind,
		       f.path, w.line, w.depth
		FROM walk w
		JOIN symbols sf ON sf.id = w.from_id
		JOIN symbols st ON st.id = w.to_id
		LEFT JOIN files f ON f.id = w.src_file_id
		ORDER BY w.depth, sf.qualified`

	return r.scanNeighborhood(ctx, q, "in", visited, seed, depth)
}

func (r *Reader) scanNeighborhood(ctx context.Context, query, dirLabel string, visited map[int64]int, seed int64, depth int) ([]NeighborEdge, []NeighborNode, error) {
	rows, err := r.db.QueryContext(ctx, query, seed, depth)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var edges []NeighborEdge
	var newNodes []NeighborNode
	for rows.Next() {
		var e NeighborEdge
		var srcPath string
		var srcLine int
		if err := rows.Scan(&e.FromID, &e.FromName, &e.ToID, &e.ToName, &e.Kind, &srcPath, &srcLine, &e.Depth); err != nil {
			return nil, nil, err
		}
		e.SrcPath = srcPath
		e.SrcLine = srcLine
		e.Direction = dirLabel
		edges = append(edges, e)

		// Record the newly-reached node on the "far" side of the edge.
		farID, _ := farSide(dirLabel, e)
		if _, ok := visited[farID]; ok {
			continue
		}
		visited[farID] = e.Depth
		nd, err := r.loadNode(ctx, farID)
		if err != nil {
			return nil, nil, err
		}
		nd.Depth = e.Depth
		newNodes = append(newNodes, nd)
	}
	return edges, newNodes, rows.Err()
}

func farSide(direction string, e NeighborEdge) (int64, string) {
	if direction == "out" {
		return e.ToID, e.ToName
	}
	return e.FromID, e.FromName
}

func (r *Reader) resolveSeed(ctx context.Context, target, project string) (int64, error) {
	if target == "" {
		return 0, nil
	}
	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return 0, err
	}
	// Prefer qualified match, fall back to a unique short-name match.
	// Both join through files to honor the optional project scope.
	qualArgs := append([]any{target}, scopeArgs...)
	var id int64
	err = r.db.QueryRowContext(ctx,
		`SELECT s.id FROM symbols s JOIN files f ON f.id = s.file_id
		 WHERE s.qualified = ?`+scope+` LIMIT 1`, qualArgs...).Scan(&id)
	if err == nil {
		return id, nil
	}
	shortArgs := append([]any{target}, scopeArgs...)
	rows, err := r.db.QueryContext(ctx,
		`SELECT s.id FROM symbols s JOIN files f ON f.id = s.file_id
		 WHERE s.name = ?`+scope+` LIMIT 2`, shortArgs...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) > 1 {
		return 0, fmt.Errorf("ambiguous target %q (multiple symbols match); pass the qualified name", target)
	}
	return 0, nil
}

func (r *Reader) loadNode(ctx context.Context, id int64) (NeighborNode, error) {
	var n NeighborNode
	n.ID = id
	err := r.db.QueryRowContext(ctx, `
		SELECT s.qualified, s.kind, f.path, s.start_line
		FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE s.id = ?`, id).Scan(&n.Qualified, &n.Kind, &n.Path, &n.StartLine)
	return n, err
}

// formatDirection is exposed for CLI convenience; string type makes the
// JSON-wire shape stable.
func (d Direction) String() string { return strings.ToLower(string(d)) }
