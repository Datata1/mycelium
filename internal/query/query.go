package query

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/datata1/mycelium/internal/focus"
)

// Reader is the query-side handle. It takes an already-open *sql.DB (owned by
// the daemon/CLI at the call site) and exposes read-only methods consumed by
// MCP, HTTP, and the CLI. This is the *only* package that issues SELECT
// queries against the index.
type Reader struct {
	db *sql.DB
	// readPreviewLines caps the verbatim line count ReadFocused returns
	// on the no-focus preview path. Tunable via SetReadPreviewLines so
	// tests can shrink it without a multi-hundred-line fixture.
	readPreviewLines int
	// probe, when set via SetProbe, lets empty results explain why a
	// path is absent (excluded / wrong extension / stale index). Nil
	// disables path diagnosis.
	probe *FSProbe
}

// DefaultReadPreviewLines is the v4-calibrated no-focus preview cap: it
// drops the no-focus byte count of a ~280-line file from heavier-than-Read
// (14 KiB) to genuinely lighter (~5 KiB).
const DefaultReadPreviewLines = 50

// NewReader returns a Reader backed by an already-open database handle.
// The caller retains ownership of db and is responsible for closing it.
func NewReader(db *sql.DB) *Reader {
	return &Reader{db: db, readPreviewLines: DefaultReadPreviewLines}
}

// SetReadPreviewLines overrides the no-focus preview cap of ReadFocused.
func (r *Reader) SetReadPreviewLines(n int) { r.readPreviewLines = n }

// FindSymbol returns symbols matching name (substring match for v0.2). FTS5
// trigram lookup lands with the switch from LIKE to MATCH once we validate
// the tokenizer across platforms.
//
// project, when non-empty, scopes results to a single workspace project
// (v1.5). An unknown project name returns zero rows rather than falling
// back to unscoped results — silent de-scoping would mislead agents.
//
// pathsIn (v1.6) additionally restricts to a fixed set of repo-relative
// paths (the `--since <ref>` filter). nil = unscoped; empty slice =
// zero rows (distinguishes "no filter" from "no changes").
//
// focus (v2.4) is a free-text hint. When non-empty, hits whose name,
// qualified name, or docstring fail the focus.Match filter are dropped,
// and survivors are re-ranked by focus score (descending). Empty focus
// preserves the v1.6 behaviour byte-for-byte.
func (r *Reader) FindSymbol(ctx context.Context, name, kind, project string, limit int, pathsIn []string, focusQ string) (FindSymbolResult, error) {
	empty := FindSymbolResult{Matches: []SymbolHit{}}
	if limit <= 0 {
		limit = 20
	}
	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return empty, err
	}
	pathClause, pathArgs, err := pathsInClause(pathsIn)
	if err != nil {
		return empty, err
	}
	q := "%" + name + "%"
	var (
		rows *sql.Rows
	)
	if kind == "" {
		args := append([]any{q, q}, scopeArgs...)
		args = append(args, pathArgs...)
		args = append(args, name, name+"%", limit+1)
		rows, err = r.db.QueryContext(ctx, `
			SELECT s.id, s.name, s.qualified, s.kind, `+displayPath+`, COALESCE(p.name, ''),
			       s.start_line, s.end_line,
			       COALESCE(s.signature, ''), COALESCE(s.docstring, '')
			FROM symbols s JOIN files f ON f.id = s.file_id
			         LEFT JOIN projects p ON p.id = f.project_id
			WHERE (s.name LIKE ? OR s.qualified LIKE ?)`+scope+pathClause+`
			ORDER BY
			  CASE WHEN s.name = ? THEN 0
			       WHEN s.name LIKE ? THEN 1 ELSE 2 END,
			  length(s.qualified)
			LIMIT ?`, args...)
	} else {
		args := append([]any{q, q, kind}, scopeArgs...)
		args = append(args, pathArgs...)
		args = append(args, limit+1)
		rows, err = r.db.QueryContext(ctx, `
			SELECT s.id, s.name, s.qualified, s.kind, `+displayPath+`, COALESCE(p.name, ''),
			       s.start_line, s.end_line,
			       COALESCE(s.signature, ''), COALESCE(s.docstring, '')
			FROM symbols s JOIN files f ON f.id = s.file_id
			         LEFT JOIN projects p ON p.id = f.project_id
			WHERE (s.name LIKE ? OR s.qualified LIKE ?) AND s.kind = ?`+scope+pathClause+`
			ORDER BY length(s.qualified)
			LIMIT ?`, args...)
	}
	if err != nil {
		return empty, err
	}
	defer rows.Close()
	hits := []SymbolHit{}
	for rows.Next() {
		var h SymbolHit
		if err := rows.Scan(&h.ID, &h.Name, &h.Qualified, &h.Kind, &h.Path, &h.Project,
			&h.StartLine, &h.EndLine, &h.Signature, &h.Docstring); err != nil {
			return empty, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return empty, err
	}
	// Queried limit+1: an extra row means the limit cut the result.
	truncated := len(hits) > limit
	if truncated {
		hits = hits[:limit]
	}
	if tokens := focus.Tokenize(focusQ); len(tokens) > 0 {
		hits = applyFocusToHits(tokens, hits)
	}
	result := FindSymbolResult{Matches: hits}
	if truncated {
		result.Hints = append(result.Hints, truncationHint(limit))
	}
	if len(hits) == 0 {
		result.Hints = append(result.Hints, r.diagnoseEmptyFind(ctx, name, kind, project)...)
	}
	return result, nil
}

// truncationHint is the uniform "result was capped" notice. Without it
// a full page is indistinguishable from a complete result — the most
// quietly misleading shape a list tool can return.
func truncationHint(limit int) string {
	return fmt.Sprintf("showing the first %d matches — more exist; pass a larger limit to see them", limit)
}

// applyFocusToHits drops hits with no focus match and reorders survivors
// by focus score (descending), preserving original order on ties so the
// SQL-side LIKE+length tiebreak still wins for equal scores.
func applyFocusToHits(tokens []string, hits []SymbolHit) []SymbolHit {
	type scored struct {
		h     SymbolHit
		score float64
		ord   int
	}
	keep := make([]scored, 0, len(hits))
	for i, h := range hits {
		score, ok := focus.MatchTokens(tokens, focus.Candidate{
			Name:      h.Name,
			Qualified: h.Qualified,
			Docstring: h.Docstring,
		})
		if !ok {
			continue
		}
		keep = append(keep, scored{h: h, score: score, ord: i})
	}
	sort.SliceStable(keep, func(i, j int) bool {
		if keep[i].score != keep[j].score {
			return keep[i].score > keep[j].score
		}
		return keep[i].ord < keep[j].ord
	})
	out := make([]SymbolHit, len(keep))
	for i, s := range keep {
		out[i] = s.h
	}
	return out
}

// GetReferences returns use-sites for a symbol. The target can be specified
// by qualified name (preferred) or short name. project filters to a single
// workspace project when non-empty. pathsIn (v1.6) scopes the src file
// path via the --since filter.
//
// Empty Matches carry Hints distinguishing "unknown symbol" from
// "symbol exists but nothing references it" — without them, "no
// references" reads as "nobody calls this", the most misleading empty
// in the product.
func (r *Reader) GetReferences(ctx context.Context, target, project string, limit int, pathsIn []string) (GetReferencesResult, error) {
	res := GetReferencesResult{Matches: []ReferenceHit{}}
	if limit <= 0 {
		limit = 100
	}
	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return res, err
	}
	pathClause, pathArgs, err := pathsInClause(pathsIn)
	if err != nil {
		return res, err
	}
	// Resolve target -> symbol ids (may be >1 for ambiguous short names).
	ids, err := r.symbolsByTarget(ctx, target)
	if err != nil {
		return res, err
	}
	var hits []ReferenceHit

	// Resolved references pointing at any of the target ids.
	if len(ids) > 0 {
		placeholders := strings.Repeat("?,", len(ids))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]interface{}, 0, len(ids)+len(scopeArgs)+len(pathArgs)+1)
		for _, id := range ids {
			args = append(args, id)
		}
		args = append(args, scopeArgs...)
		args = append(args, pathArgs...)
		args = append(args, limit+1)
		rows, err := r.db.QueryContext(ctx, `
			SELECT r.id, `+displayPath+`, COALESCE(p.name, ''), r.line, r.col,
			       COALESCE(r.src_symbol_id, 0), COALESCE(ss.qualified, ''),
			       r.dst_name, COALESCE(r.dst_symbol_id, 0), r.kind, r.resolved
			FROM refs r
			JOIN files f ON f.id = r.src_file_id
			LEFT JOIN projects p ON p.id = f.project_id
			LEFT JOIN symbols ss ON ss.id = r.src_symbol_id
			WHERE r.dst_symbol_id IN (`+placeholders+`)`+scope+pathClause+`
			ORDER BY 2, r.line
			LIMIT ?`, args...)
		if err != nil {
			return res, err
		}
		hits, err = scanReferenceHits(rows, hits)
		if err != nil {
			return res, err
		}
	}

	// Queried limit+1: an extra row means the cap cut the resolved set,
	// in which case the textual fallback below is skipped entirely.
	truncated := len(hits) > limit
	if truncated {
		hits = hits[:limit]
	}

	// Textual-only fallback: refs unresolved but whose dst_name or dst_short
	// equals the target. Useful when the target isn't defined in this repo
	// (e.g. stdlib calls) or when resolution was ambiguous.
	remaining := limit - len(hits)
	if remaining > 0 {
		args := []any{target, target}
		args = append(args, scopeArgs...)
		args = append(args, pathArgs...)
		args = append(args, remaining+1)
		rows, err := r.db.QueryContext(ctx, `
			SELECT r.id, `+displayPath+`, COALESCE(p.name, ''), r.line, r.col,
			       COALESCE(r.src_symbol_id, 0), COALESCE(ss.qualified, ''),
			       r.dst_name, COALESCE(r.dst_symbol_id, 0), r.kind, r.resolved
			FROM refs r
			JOIN files f ON f.id = r.src_file_id
			LEFT JOIN projects p ON p.id = f.project_id
			LEFT JOIN symbols ss ON ss.id = r.src_symbol_id
			WHERE r.resolved = 0 AND (r.dst_name = ? OR r.dst_short = ?)`+scope+pathClause+`
			ORDER BY 2, r.line
			LIMIT ?`, args...)
		if err != nil {
			return res, err
		}
		hits, err = scanReferenceHits(rows, hits)
		if err != nil {
			return res, err
		}
	}

	if len(hits) > limit {
		hits = hits[:limit]
		truncated = true
	}
	if hits != nil {
		res.Matches = hits
	}
	if truncated {
		res.Hints = append(res.Hints, truncationHint(limit))
	}
	if len(res.Matches) == 0 && project == "" && pathsIn == nil {
		// Explain the miss only when no filter could be the cause —
		// filtered empties are usually the filter, not the symbol.
		_, hasScan, _ := r.LastFullScanAt(ctx)
		res.Hints = buildRefsHints(target, len(ids), !hasScan)
	}
	return res, nil
}

// symbolsByTarget resolves a target name to symbol IDs for use in the
// get_references first-pass query. It returns both direct matches (qualified or
// short name) and, for any class/interface symbol found, all of its child
// symbols (methods, statics). This ensures that CsEnv.from() and
// CsEnv.empty() calls — resolved by the TS resolver to the *method* symbols
// rather than the *class* symbol — are visible when the caller searches for
// references to the class name "CsEnv".
func (r *Reader) symbolsByTarget(ctx context.Context, target string) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, qualified FROM symbols WHERE qualified = ? OR name = ?`, target, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	var qualifieds []string
	for rows.Next() {
		var id int64
		var qualified string
		if err := rows.Scan(&id, &qualified); err != nil {
			return nil, err
		}
		ids = append(ids, id)
		qualifieds = append(qualifieds, qualified)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// For each matched symbol, include child symbols so that
	// "ClassName.method()" calls (resolved to the method symbol) are
	// visible when the parent class is the target.
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, q := range qualifieds {
		childRows, err := r.db.QueryContext(ctx,
			`SELECT id FROM symbols WHERE qualified LIKE ?`, q+".%")
		if err != nil {
			return nil, err
		}
		for childRows.Next() {
			var id int64
			if err := childRows.Scan(&id); err != nil {
				childRows.Close()
				return nil, err
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if err := childRows.Err(); err != nil {
			childRows.Close()
			return nil, err
		}
		childRows.Close()
	}
	return ids, nil
}

func scanReferenceHits(rows *sql.Rows, acc []ReferenceHit) ([]ReferenceHit, error) {
	defer rows.Close()
	for rows.Next() {
		var h ReferenceHit
		var resolved int
		if err := rows.Scan(&h.ID, &h.SrcPath, &h.SrcProject, &h.SrcLine, &h.SrcCol,
			&h.SrcSymbolID, &h.SrcSymbolName, &h.DstName, &h.DstSymbolID,
			&h.Kind, &resolved); err != nil {
			return acc, err
		}
		h.Resolved = resolved == 1
		acc = append(acc, h)
	}
	return acc, rows.Err()
}

// ListFiles returns files matching an optional language filter and name
// substring. Globs can be layered by the CLI if richer matching is needed.
// pathsIn (v1.6) scopes to the --since path set.
//
// Returns the FindSymbolResult-style envelope so a capped result can say
// so via Hints instead of silently looking complete.
func (r *Reader) ListFiles(ctx context.Context, language, nameContains, project string, limit int, pathsIn []string) (ListFilesResult, error) {
	res := ListFilesResult{Matches: []FileHit{}}
	if limit <= 0 {
		limit = 500
	}
	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return res, err
	}
	pathClause, pathArgs, err := pathsInClause(pathsIn)
	if err != nil {
		return res, err
	}
	args := []interface{}{}
	conds := []string{}
	if language != "" {
		conds = append(conds, "f.language = ?")
		args = append(args, language)
	}
	if nameContains != "" {
		conds = append(conds, "("+displayPath+") LIKE ?")
		args = append(args, "%"+nameContains+"%")
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	appendScope := func(clause string, cargs []any) {
		if clause == "" {
			return
		}
		if where == "" {
			// Turn leading " AND " into "WHERE ".
			where = "WHERE " + clause[len(" AND "):]
		} else {
			where += clause
		}
		args = append(args, cargs...)
	}
	appendScope(scope, scopeArgs)
	appendScope(pathClause, pathArgs)
	args = append(args, limit+1)
	q := fmt.Sprintf(`
		SELECT `+displayPath+`, COALESCE(p.name, ''), f.language, f.size_bytes, f.last_indexed_at,
		       (SELECT COUNT(*) FROM symbols s WHERE s.file_id = f.id)
		FROM files f LEFT JOIN projects p ON p.id = f.project_id
		%s ORDER BY 1 LIMIT ?`, where)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var h FileHit
		var ts int64
		if err := rows.Scan(&h.Path, &h.Project, &h.Language, &h.SizeBytes, &ts, &h.SymbolCount); err != nil {
			return res, err
		}
		h.LastIndexed = time.Unix(ts, 0)
		res.Matches = append(res.Matches, h)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(res.Matches) > limit {
		res.Matches = res.Matches[:limit]
		res.Hints = append(res.Hints, truncationHint(limit))
	}
	return res, nil
}

// GetFileOutline returns the hierarchical symbol tree for a file. Parent
// relationships drive the tree; parentless symbols sit at the top level.
//
// focus (v2.4) — when non-empty, top-level items are kept iff they or any
// descendant matches focus. Empty focus preserves prior behaviour.
func (r *Reader) GetFileOutline(ctx context.Context, path, focusQ string) ([]FileOutlineItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.name, s.qualified, s.kind, s.start_line, s.end_line,
		       COALESCE(s.signature, ''), COALESCE(s.parent_id, 0)
		FROM symbols s JOIN files f ON f.id = s.file_id
		         LEFT JOIN projects p ON p.id = f.project_id
		WHERE f.path = ?
		   OR (p.root IS NOT NULL AND ? = p.root || '/' || f.path)
		ORDER BY s.start_line`, path, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type row struct {
		FileOutlineItem
		ParentID int64
	}
	all := map[int64]*row{}
	var order []int64
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.SymbolID, &rr.Name, &rr.Qualified, &rr.Kind, &rr.StartLine, &rr.EndLine, &rr.Signature, &rr.ParentID); err != nil {
			return nil, err
		}
		all[rr.SymbolID] = &rr
		order = append(order, rr.SymbolID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Zero symbols: distinguish "file not indexed" (error with
	// suggestions + probe diagnosis) from "file indexed but symbol-free"
	// (legitimate empty) — the bare empty was indistinguishable and read
	// as tool failure.
	if len(order) == 0 {
		var n int
		if err := r.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM files f LEFT JOIN projects p ON p.id = f.project_id
			WHERE f.path = ? OR (p.root IS NOT NULL AND ? = p.root || '/' || f.path)`,
			path, path).Scan(&n); err == nil && n == 0 {
			return nil, notFound("file not in index: %s%s%s",
				path, formatPathSuggestions(suggestPaths(ctx, r.db, path, 3)),
				joinDiagnosis(r.diagnosePath(ctx, path)))
		}
		return nil, nil
	}

	var out []FileOutlineItem
	for _, id := range order {
		rr := all[id]
		if rr.ParentID == 0 || all[rr.ParentID] == nil {
			out = append(out, rr.FileOutlineItem)
		}
	}
	// Attach children in a second pass.
	idx := make(map[int64]*FileOutlineItem, len(out))
	for i := range out {
		idx[out[i].SymbolID] = &out[i]
	}
	for _, id := range order {
		rr := all[id]
		if rr.ParentID == 0 {
			continue
		}
		if parent, ok := idx[rr.ParentID]; ok {
			parent.Children = append(parent.Children, rr.FileOutlineItem)
		}
	}
	if tokens := focus.Tokenize(focusQ); len(tokens) > 0 {
		out = filterOutlineByFocus(tokens, out)
	}
	return out, nil
}

// filterOutlineByFocus drops top-level items whose subtree (item +
// children, recursively) contains no focus match. Children of a kept
// item are not pruned — once an item earns its place, the agent gets
// the full sub-outline.
func filterOutlineByFocus(tokens []string, items []FileOutlineItem) []FileOutlineItem {
	var keep []FileOutlineItem
	for _, it := range items {
		if outlineMatches(tokens, it) {
			keep = append(keep, it)
		}
	}
	return keep
}

func outlineMatches(tokens []string, it FileOutlineItem) bool {
	if _, ok := focus.MatchTokens(tokens, focus.Candidate{
		Name:      it.Name,
		Qualified: it.Qualified,
	}); ok {
		return true
	}
	for _, c := range it.Children {
		if outlineMatches(tokens, c) {
			return true
		}
	}
	return false
}

// Stats returns a point-in-time snapshot of the index state. Queries run
// under ctx and cancel immediately when it is done.
func (r *Reader) Stats(ctx context.Context) (Stats, error) {
	s := Stats{
		ByKind:               map[string]int{},
		ByLang:               map[string]int{},
		UnresolvedByLanguage: map[string]int{},
		TotalByLanguage:      map[string]int{},
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files`).Scan(&s.Files); err != nil {
		return s, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols`).Scan(&s.Symbols); err != nil {
		return s, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refs`).Scan(&s.Refs); err != nil {
		return s, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refs WHERE resolved = 1`).Scan(&s.Resolved); err != nil {
		return s, err
	}
	// Self-loops come in two flavors:
	//   resolver_version=0: v1.1-style resolution artifact (ix.db.Close()
	//     matching our Index.Close via unique short name). Target: 0.
	//   resolver_version>=1: real recursion (callTargetName(x.X) genuinely
	//     calls callTargetName). Informational; not a bug.
	// SelfLoopCount tracks only the resolution-bug variant — the one the
	// v1.2 type resolver was meant to kill.
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM refs
		 WHERE src_symbol_id IS NOT NULL
		   AND src_symbol_id = dst_symbol_id
		   AND resolver_version = 0`,
	).Scan(&s.SelfLoopCount); err != nil {
		return s, err
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM refs
		 WHERE src_symbol_id IS NOT NULL
		   AND src_symbol_id = dst_symbol_id
		   AND resolver_version >= 1`,
	).Scan(&s.RecursionSelfLoops); err != nil {
		return s, err
	}
	// v1.2 refs breakdown — the honest quality signal lives here.
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refs WHERE kind != 'import'`).Scan(&s.NonImportRefs)
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refs WHERE resolver_version >= 1`).Scan(&s.RefsTypeResolved)
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refs WHERE resolver_version >= 1 AND dst_symbol_id IS NULL`).Scan(&s.RefsExternalKnown)
	_ = r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM refs
		 WHERE kind != 'import'
		   AND resolver_version = 0
		   AND dst_symbol_id IS NULL`).Scan(&s.RefsTrulyUnresolved)

	// Per-language unresolved — same definition (v0 + no dst + not import).
	rows, err := r.db.QueryContext(ctx, `
		SELECT f.language, COUNT(*)
		FROM refs r JOIN files f ON f.id = r.src_file_id
		WHERE r.kind != 'import' AND r.resolver_version = 0 AND r.dst_symbol_id IS NULL
		GROUP BY f.language`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var lang string
		var n int
		if err := rows.Scan(&lang, &n); err != nil {
			rows.Close()
			return s, err
		}
		s.UnresolvedByLanguage[lang] = n
	}
	rows.Close()

	// Per-language total of non-import refs — the denominator for
	// unresolved_by_language in the doctor.
	rows, err = r.db.QueryContext(ctx, `
		SELECT f.language, COUNT(*)
		FROM refs r JOIN files f ON f.id = r.src_file_id
		WHERE r.kind != 'import'
		GROUP BY f.language`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var lang string
		var n int
		if err := rows.Scan(&lang, &n); err != nil {
			rows.Close()
			return s, err
		}
		s.TotalByLanguage[lang] = n
	}
	rows.Close()

	// v2.1: interface-implementer linkage. RefInherit edges link concrete
	// types to interfaces they implement; agents querying upstream
	// consumers depend on these to reach interface-typed callers.
	_ = r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM refs WHERE kind = 'inherit'`,
	).Scan(&s.InterfaceImplementsRefs)
	_ = r.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT src_symbol_id) FROM refs WHERE kind = 'inherit'`,
	).Scan(&s.InterfaceConcreteTypes)

	// v3.3: per-kind document entry counts. Empty result means no
	// document parsers fired; doctor reads this alongside on-disk
	// candidate files to decide whether a "no entries" reading is
	// a real problem.
	if docRows, err := r.db.QueryContext(ctx,
		`SELECT kind, COUNT(*) FROM documents GROUP BY kind`); err == nil {
		s.DocumentsByKind = map[string]int{}
		for docRows.Next() {
			var k string
			var n int
			if err := docRows.Scan(&k, &n); err == nil {
				s.DocumentsByKind[k] = n
			}
		}
		docRows.Close()
	}

	// v3.1: configured projects with per-project file counts. LEFT JOIN
	// so projects with zero matched files (the misconfiguration we
	// want to flag) still appear in the output with file_count=0.
	if projRows, err := r.db.QueryContext(ctx, `
		SELECT p.name, p.root, COUNT(f.id)
		FROM projects p
		LEFT JOIN files f ON f.project_id = p.id
		GROUP BY p.id
		ORDER BY p.name`); err == nil {
		for projRows.Next() {
			var ps ProjectStats
			if err := projRows.Scan(&ps.Name, &ps.Root, &ps.FileCount); err == nil {
				s.ConfiguredProjects = append(s.ConfiguredProjects, ps)
			}
		}
		projRows.Close()
	}

	// SQLite page stats. freelist_count / page_count is a cheap
	// fragmentation proxy that tells VACUUM whether it'd pay off.
	_ = r.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&s.DBFreelistPages)
	_ = r.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&s.DBPageCount)
	var pageSize int
	_ = r.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize)
	s.DBSizeBytes = int64(s.DBPageCount) * int64(pageSize)

	rows, err = r.db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM symbols GROUP BY kind`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return s, err
		}
		s.ByKind[k] = n
	}
	rows.Close()

	rows, err = r.db.QueryContext(ctx, `SELECT language, COUNT(*) FROM files GROUP BY language`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var l string
		var n int
		if err := rows.Scan(&l, &n); err != nil {
			return s, err
		}
		s.ByLang[l] = n
	}
	var ts sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_indexed_at), 0) FROM files`).Scan(&ts); err == nil && ts.Valid && ts.Int64 > 0 {
		s.LastScan = time.Unix(ts.Int64, 0)
	}
	if t, ok, err := r.LastFullScanAt(ctx); err == nil && ok {
		s.LastFullScan = t
	}
	return s, nil
}
