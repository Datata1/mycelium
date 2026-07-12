package query

import (
	"context"
	"fmt"
	"sort"
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
		return result, notFound("symbol not found: %q", target)
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
		SELECT s.id, s.qualified, s.kind, ` + displayPath + `, COALESCE(p.name, ''),
		       s.start_line, MIN(w.depth) AS distance
		FROM walk w
		JOIN symbols s ON s.id = w.symbol_id
		JOIN files f ON f.id = s.file_id
		LEFT JOIN projects p ON p.id = f.project_id
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
		if err := rows.Scan(&h.ID, &h.Qualified, &h.Kind, &h.Path, &h.Project, &h.StartLine, &h.Distance); err != nil {
			return result, err
		}
		result.Hits = append(result.Hits, h)
	}
	return result, rows.Err()
}

// CriticalPath returns up to k shortest outbound paths from `from` to
// `to`. Walks refs in the "calls" direction; swap the two args to ask
// the inverse question.
//
// Depth defaults to MaxCriticalPathDepth (8). The walk is a layered
// BFS in Go — one indexed query per layer over a visited set — so cost
// is bounded by the reachable edge count, not the path count. The
// earlier recursive-CTE version enumerated every acyclic path
// (~fanout^depth rows) before its LIMIT could apply, which measured
// ~24s at depth 8 on a fanout-8 synthetic graph. All returned paths
// share the minimal hop count; k caps how many of them are listed.
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
		return result, notFound("from symbol not found: %q", from)
	}
	toID, err := r.resolveSeed(ctx, to, project)
	if err != nil {
		return result, err
	}
	if toID == 0 {
		return result, notFound("to symbol not found: %q", to)
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

	parsed, err := r.shortestPaths(ctx, fromID, toID, depth, k)
	if err != nil {
		return result, err
	}
	if len(parsed) == 0 {
		return result, nil
	}

	// Hydrate every distinct vertex in one round-trip, then stitch
	// back into path order. N+1 queries at depth 8 × k 5 would hurt.
	idSet := map[int64]struct{}{}
	for _, ids := range parsed {
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
		SELECT s.id, s.qualified, s.kind, `+displayPath+`, COALESCE(p.name, ''), s.start_line
		FROM symbols s JOIN files f ON f.id = s.file_id
		         LEFT JOIN projects p ON p.id = f.project_id
		WHERE s.id IN (`+placeholders+`)`, idList...)
	if err != nil {
		return result, err
	}
	defer vRows.Close()
	byID := make(map[int64]PathVertex, len(idList))
	for vRows.Next() {
		var v PathVertex
		if err := vRows.Scan(&v.ID, &v.Qualified, &v.Kind, &v.Path, &v.Project, &v.StartLine); err != nil {
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

// shortestPaths runs a layered BFS from fromID over outbound refs and
// returns up to k minimal-length paths to toID as vertex-id slices in
// from→to order. Nil when toID is unreachable within maxDepth hops.
//
// Each node keeps the set of predecessors that reached it at its
// discovery depth (a shortest-path DAG), so every minimal path is
// enumerable afterwards without ever materializing non-minimal ones.
func (r *Reader) shortestPaths(ctx context.Context, fromID, toID int64, maxDepth, k int) ([][]int64, error) {
	visitedDepth := map[int64]int{fromID: 0}
	parents := map[int64][]int64{}
	frontier := []int64{fromID}
	found := false
	for depth := 1; depth <= maxDepth && !found && len(frontier) > 0; depth++ {
		edges, err := r.outboundEdges(ctx, frontier)
		if err != nil {
			return nil, err
		}
		next := map[int64]struct{}{}
		for _, e := range edges {
			d, seen := visitedDepth[e.dst]
			switch {
			case !seen:
				visitedDepth[e.dst] = depth
				parents[e.dst] = append(parents[e.dst], e.src)
				next[e.dst] = struct{}{}
			case d == depth:
				// A second minimal-length route into a node discovered
				// this layer — keep it so path enumeration sees every
				// shortest path. Deeper routes into already-visited
				// nodes are non-minimal and dropped.
				parents[e.dst] = append(parents[e.dst], e.src)
			}
		}
		if _, ok := next[toID]; ok {
			found = true
		}
		frontier = frontier[:0]
		for id := range next {
			frontier = append(frontier, id)
		}
	}
	if !found {
		return nil, nil
	}

	// Walk the parent DAG back from the target. Parent lists are
	// sorted for deterministic output (map iteration randomizes the
	// append order above).
	for _, ps := range parents {
		sort.Slice(ps, func(i, j int) bool { return ps[i] < ps[j] })
	}
	var paths [][]int64
	stack := []int64{}
	var walk func(id int64)
	walk = func(id int64) {
		if len(paths) >= k {
			return
		}
		stack = append(stack, id)
		defer func() { stack = stack[:len(stack)-1] }()
		if id == fromID {
			p := make([]int64, len(stack))
			for i, v := range stack {
				p[len(stack)-1-i] = v
			}
			paths = append(paths, p)
			return
		}
		for _, parent := range parents[id] {
			walk(parent)
		}
	}
	walk(toID)
	return paths, nil
}

type refEdge struct {
	src, dst int64
}

// outboundEdges returns the distinct resolved (src, dst) pairs leaving
// the frontier. Chunked at 500 ids to stay under SQLite's 999-parameter
// limit, mirroring the prune path in internal/index.
func (r *Reader) outboundEdges(ctx context.Context, frontier []int64) ([]refEdge, error) {
	const chunk = 500
	var edges []refEdge
	for start := 0; start < len(frontier); start += chunk {
		ids := frontier[start:min(start+chunk, len(frontier))]
		placeholders := "?" + strings.Repeat(",?", len(ids)-1)
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err := r.db.QueryContext(ctx, `
			SELECT DISTINCT r.src_symbol_id, r.dst_symbol_id
			FROM refs r
			WHERE r.dst_symbol_id IS NOT NULL
			  AND r.src_symbol_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var e refEdge
			if err := rows.Scan(&e.src, &e.dst); err != nil {
				rows.Close()
				return nil, err
			}
			edges = append(edges, e)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return edges, nil
}

func nodeAsVertex(n NeighborNode) PathVertex {
	return PathVertex{
		ID: n.ID, Qualified: n.Qualified, Kind: n.Kind,
		Path: n.Path, Project: n.Project, StartLine: n.StartLine,
	}
}

// ClosureFileHit is one file reached by the multi-seed inbound walk —
// the raw material for test selection.
type ClosureFileHit struct {
	Path     string // repo-relative (displayPath)
	Project  string
	Distance int // 1 = file contains a direct caller of a seed
}

// InboundClosureFiles walks the inbound ref graph from ALL seed symbols
// at once (the multi-seed sibling of ImpactAnalysis) and returns the
// files containing any symbol in the closure, each at its minimum
// distance. Seeds are chunked to respect SQLite's parameter cap;
// results merge across chunks by min distance.
func (r *Reader) InboundClosureFiles(ctx context.Context, seedIDs []int64, depth int) ([]ClosureFileHit, error) {
	if len(seedIDs) == 0 {
		return []ClosureFileHit{}, nil
	}
	if depth <= 0 {
		depth = DefaultImpactDepth
	}
	if depth > MaxImpactDepth {
		depth = MaxImpactDepth
	}

	type key struct{ path, project string }
	best := map[key]int{}
	const chunk = 500
	for start := 0; start < len(seedIDs); start += chunk {
		end := start + chunk
		if end > len(seedIDs) {
			end = len(seedIDs)
		}
		part := seedIDs[start:end]
		placeholders := "?" + strings.Repeat(",?", len(part)-1)
		args := make([]any, 0, len(part)+1)
		for _, id := range part {
			args = append(args, id)
		}
		args = append(args, depth)

		rows, err := r.db.QueryContext(ctx, `
			WITH RECURSIVE walk(symbol_id, depth) AS (
				SELECT r.src_symbol_id, 1
				FROM refs r
				WHERE r.dst_symbol_id IN (`+placeholders+`) AND r.src_symbol_id IS NOT NULL
			  UNION
				SELECT r.src_symbol_id, w.depth + 1
				FROM refs r
				JOIN walk w ON r.dst_symbol_id = w.symbol_id
				WHERE r.src_symbol_id IS NOT NULL AND w.depth < ?
			)
			SELECT `+displayPath+`, COALESCE(p.name, ''), MIN(w.depth)
			FROM walk w
			JOIN symbols s ON s.id = w.symbol_id
			JOIN files f ON f.id = s.file_id
			LEFT JOIN projects p ON p.id = f.project_id
			GROUP BY 1, 2`, args...)
		if err != nil {
			return nil, fmt.Errorf("inbound closure: %w", err)
		}
		for rows.Next() {
			var k key
			var d int
			if err := rows.Scan(&k.path, &k.project, &d); err != nil {
				rows.Close()
				return nil, err
			}
			if cur, ok := best[k]; !ok || d < cur {
				best[k] = d
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	out := make([]ClosureFileHit, 0, len(best))
	for k, d := range best {
		out = append(out, ClosureFileHit{Path: k.path, Project: k.project, Distance: d})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}
