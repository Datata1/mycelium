package query

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// MaxImpactDepth caps how deep ImpactAnalysis will walk the inbound
// ref graph. Depth 10 is the honest upper bound: beyond that the
// transitive closure on a dense graph degenerates into "every caller".
const MaxImpactDepth = 10

// DefaultImpactDepth is two hops deeper than get_neighborhood's default
// — impact analysis exists to go further.
const DefaultImpactDepth = 5

// MaxCriticalPathDepth caps the BFS in CriticalPath. Eight hops is a
// practical ceiling: if the answer is further, the query shape is
// wrong.
const MaxCriticalPathDepth = 8

// DefaultCriticalPathK is the fallback for how many paths to return
// when the caller doesn't specify.
const DefaultCriticalPathK = 5

// ImpactHit is one transitive caller of the seed symbol, ranked by
// distance. Lower distance = closer to the seed (distance 1 = direct
// caller).
type ImpactHit struct {
	ID        int64  `json:"id"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	Distance  int    `json:"distance"`
}

// Impact is the result of ImpactAnalysis. Notes mirrors the
// Neighborhood convention for surfacing depth-clamp messages.
type Impact struct {
	Seed  NeighborNode `json:"seed"`
	Hits  []ImpactHit  `json:"hits"`
	Notes []string     `json:"notes,omitempty"`
}

// ImpactAnalysis returns the transitive inbound closure around the
// target symbol — every symbol that directly or transitively calls
// into it, ranked by shortest-distance.
//
// Defaults: depth 5 (deliberately deeper than get_neighborhood's 2),
// hard ceiling 10. Optional kind filter narrows the reported set; a
// typical use is kind="method"/"function" to surface test coverage.
//
// project and pathsIn scope the *reported* caller set — we still walk
// the full graph so cross-project chains surface, but only callers
// whose owning file matches the filter are returned. This mirrors
// get_neighborhood's "scope the seed, not the walk" semantics.
func (r *Reader) ImpactAnalysis(ctx context.Context, target, kind, project string, depth int, pathsIn []string) (Impact, error) {
	var result Impact
	requestedDepth := depth
	if depth <= 0 {
		depth = DefaultImpactDepth
	}
	if depth > MaxImpactDepth {
		depth = MaxImpactDepth
		result.Notes = append(result.Notes, fmt.Sprintf(
			"depth clamped from %d to %d (see LIMITATIONS.md#graph-queries)",
			requestedDepth, MaxImpactDepth,
		))
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

	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return result, err
	}
	pathClause, pathArgs, err := pathsInClause(pathsIn)
	if err != nil {
		return result, err
	}

	// Inbound recursive CTE: start from refs pointing AT the seed,
	// then walk src -> src transitively. UNION (not UNION ALL) dedupes
	// (src, depth) pairs; the outer GROUP BY id + MIN(depth) collapses
	// that to one row per caller at its shortest distance.
	kindClause := ""
	args := []any{seedID, depth}
	if kind != "" {
		kindClause = " AND s.kind = ?"
		args = append(args, kind)
	}
	args = append(args, scopeArgs...)
	args = append(args, pathArgs...)
	q := `
		WITH RECURSIVE walk(symbol_id, depth) AS (
		    SELECT r.src_symbol_id, 1
		    FROM refs r
		    WHERE r.dst_symbol_id = ? AND r.src_symbol_id IS NOT NULL
		  UNION
		    SELECT r.src_symbol_id, w.depth + 1
		    FROM refs r
		    JOIN walk w ON r.dst_symbol_id = w.symbol_id
		    WHERE r.src_symbol_id IS NOT NULL AND w.depth < ?
		)
		SELECT s.id, s.qualified, s.kind, f.path, s.start_line,
		       MIN(w.depth) AS distance
		FROM walk w
		JOIN symbols s ON s.id = w.symbol_id
		JOIN files f ON f.id = s.file_id
		WHERE 1=1` + kindClause + scope + pathClause + `
		GROUP BY s.id
		ORDER BY distance, s.qualified`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var h ImpactHit
		if err := rows.Scan(&h.ID, &h.Qualified, &h.Kind, &h.Path, &h.StartLine, &h.Distance); err != nil {
			return result, err
		}
		result.Hits = append(result.Hits, h)
	}
	return result, rows.Err()
}

// PathVertex is one step in a CriticalPath result.
type PathVertex struct {
	ID        int64  `json:"id"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
}

// CriticalPathResult carries one or more shortest outbound paths from
// the `from` seed to the `to` target. Empty Paths means no route was
// found within the depth cap.
type CriticalPathResult struct {
	From  NeighborNode   `json:"from"`
	To    NeighborNode   `json:"to"`
	Paths [][]PathVertex `json:"paths"`
	Notes []string       `json:"notes,omitempty"`
}

// CriticalPath returns up to k shortest outbound paths from `from` to
// `to`. Walks refs in the "calls" direction; swap the two args to ask
// the inverse question.
//
// Depth defaults to MaxCriticalPathDepth (8). Cycle prevention uses
// the SQLite instr() idiom on an accumulated path column.
//
// project (v1.5) scopes *only* the seed lookups — the traversal is
// global so useful paths through one project into another surface
// naturally. Workspace-mode users want that, not strict isolation.
func (r *Reader) CriticalPath(ctx context.Context, from, to, project string, depth, k int) (CriticalPathResult, error) {
	var result CriticalPathResult
	requestedDepth := depth
	if depth <= 0 {
		depth = MaxCriticalPathDepth
	}
	if depth > MaxCriticalPathDepth {
		depth = MaxCriticalPathDepth
		result.Notes = append(result.Notes, fmt.Sprintf(
			"depth clamped from %d to %d (see LIMITATIONS.md#graph-queries)",
			requestedDepth, MaxCriticalPathDepth,
		))
	}
	if k <= 0 {
		k = DefaultCriticalPathK
	}

	fromID, err := r.resolveSeed(ctx, from, project)
	if err != nil {
		return result, err
	}
	if fromID == 0 {
		return result, fmt.Errorf("from symbol not found: %q", from)
	}
	toID, err := r.resolveSeed(ctx, to, project)
	if err != nil {
		return result, err
	}
	if toID == 0 {
		return result, fmt.Errorf("to symbol not found: %q", to)
	}
	fromNode, err := r.loadNode(ctx, fromID)
	if err != nil {
		return result, err
	}
	toNode, err := r.loadNode(ctx, toID)
	if err != nil {
		return result, err
	}
	result.From = fromNode
	result.To = toNode

	if fromID == toID {
		// Zero-hop path: just the seed itself.
		v := nodeAsVertex(fromNode)
		result.Paths = [][]PathVertex{{v}}
		return result, nil
	}

	// The path column stores IDs bracketed by commas — ",1,2,3," —
	// so instr(path, ',NNN,') cheaply checks membership without false
	// matches on substring IDs (e.g. id=1 vs id=123).
	q := `
		WITH RECURSIVE walk(from_id, to_id, depth, path) AS (
		    SELECT r.src_symbol_id, r.dst_symbol_id, 1,
		           ',' || r.src_symbol_id || ',' || r.dst_symbol_id || ','
		    FROM refs r
		    WHERE r.src_symbol_id = ? AND r.dst_symbol_id IS NOT NULL
		  UNION ALL
		    SELECT r.src_symbol_id, r.dst_symbol_id, w.depth + 1,
		           w.path || r.dst_symbol_id || ','
		    FROM refs r
		    JOIN walk w ON r.src_symbol_id = w.to_id
		    WHERE r.dst_symbol_id IS NOT NULL
		      AND w.depth < ?
		      AND instr(w.path, ',' || r.dst_symbol_id || ',') = 0
		)
		SELECT depth, path
		FROM walk
		WHERE to_id = ?
		ORDER BY depth
		LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, fromID, depth, toID, k)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	var rawPaths []string
	for rows.Next() {
		var depthCol int
		var pathStr string
		if err := rows.Scan(&depthCol, &pathStr); err != nil {
			return result, err
		}
		rawPaths = append(rawPaths, pathStr)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(rawPaths) == 0 {
		return result, nil
	}

	// Hydrate every distinct vertex in one round-trip, then stitch
	// back into path order. N+1 queries at depth 8 × k 5 would hurt.
	idSet := map[int64]struct{}{}
	parsed := make([][]int64, 0, len(rawPaths))
	for _, p := range rawPaths {
		ids := splitPath(p)
		parsed = append(parsed, ids)
		for _, id := range ids {
			idSet[id] = struct{}{}
		}
	}
	idList := make([]any, 0, len(idSet))
	for id := range idSet {
		idList = append(idList, id)
	}
	placeholders := "?" + strings.Repeat(",?", len(idList)-1)
	vRows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.qualified, s.kind, f.path, s.start_line
		FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE s.id IN (`+placeholders+`)`, idList...)
	if err != nil {
		return result, err
	}
	defer vRows.Close()
	byID := make(map[int64]PathVertex, len(idList))
	for vRows.Next() {
		var v PathVertex
		if err := vRows.Scan(&v.ID, &v.Qualified, &v.Kind, &v.Path, &v.StartLine); err != nil {
			return result, err
		}
		byID[v.ID] = v
	}
	if err := vRows.Err(); err != nil {
		return result, err
	}
	for _, ids := range parsed {
		path := make([]PathVertex, 0, len(ids))
		for _, id := range ids {
			path = append(path, byID[id])
		}
		result.Paths = append(result.Paths, path)
	}
	return result, nil
}

func splitPath(s string) []int64 {
	s = strings.Trim(s, ",")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	return out
}

func nodeAsVertex(n NeighborNode) PathVertex {
	return PathVertex{
		ID: n.ID, Qualified: n.Qualified, Kind: n.Kind,
		Path: n.Path, StartLine: n.StartLine,
	}
}
