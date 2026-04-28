package query

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jdwiederstein/mycelium/internal/focus"
)

// Reader is the query-side handle. It takes an already-open *sql.DB (owned by
// the daemon/CLI at the call site) and exposes read-only methods consumed by
// MCP, HTTP, and the CLI. This is the *only* package that issues SELECT
// queries against the index.
type Reader struct {
	db *sql.DB
}

func NewReader(db *sql.DB) *Reader { return &Reader{db: db} }

// SymbolHit is the canonical shape returned by symbol-producing queries.
type SymbolHit struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Signature string `json:"signature,omitempty"`
	Docstring string `json:"docstring,omitempty"`
}

// FindSymbolResult wraps the matches plus optional diagnostic hints
// (v3.1). Hints populate when Matches is empty *and* the diagnose path
// has something useful to say about why — typo'd project name,
// kind filter that eliminated all real matches, etc. Empty Hints on an
// empty Matches means "the index just doesn't contain that symbol."
//
// Always returned with non-nil Matches (empty slice rather than nil) so
// JSON serialisation produces `[]` instead of `null` — agents
// distinguish "tool returned nothing" from "tool errored" more reliably
// when the shape is consistent.
type FindSymbolResult struct {
	Matches []SymbolHit `json:"matches"`
	Hints   []string    `json:"hints,omitempty"`
}

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
		args = append(args, name, name+"%", limit)
		rows, err = r.db.QueryContext(ctx, `
			SELECT s.id, s.name, s.qualified, s.kind, f.path, s.start_line, s.end_line,
			       COALESCE(s.signature, ''), COALESCE(s.docstring, '')
			FROM symbols s JOIN files f ON f.id = s.file_id
			WHERE (s.name LIKE ? OR s.qualified LIKE ?)`+scope+pathClause+`
			ORDER BY
			  CASE WHEN s.name = ? THEN 0
			       WHEN s.name LIKE ? THEN 1 ELSE 2 END,
			  length(s.qualified)
			LIMIT ?`, args...)
	} else {
		args := append([]any{q, q, kind}, scopeArgs...)
		args = append(args, pathArgs...)
		args = append(args, limit)
		rows, err = r.db.QueryContext(ctx, `
			SELECT s.id, s.name, s.qualified, s.kind, f.path, s.start_line, s.end_line,
			       COALESCE(s.signature, ''), COALESCE(s.docstring, '')
			FROM symbols s JOIN files f ON f.id = s.file_id
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
		if err := rows.Scan(&h.ID, &h.Name, &h.Qualified, &h.Kind, &h.Path, &h.StartLine, &h.EndLine, &h.Signature, &h.Docstring); err != nil {
			return empty, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return empty, err
	}
	if tokens := focus.Tokenize(focusQ); len(tokens) > 0 {
		hits = applyFocusToHits(tokens, hits)
	}
	result := FindSymbolResult{Matches: hits}
	if len(hits) == 0 {
		result.Hints = r.diagnoseEmptyFind(ctx, name, kind, project)
	}
	return result, nil
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

// ReferenceHit is one call/import/type-use site pointing at a symbol.
// Resolved=true means dst_symbol_id is populated; otherwise the hit is
// a textual-only match on dst_name.
type ReferenceHit struct {
	ID             int64  `json:"id"`
	SrcPath        string `json:"src_path"`
	SrcLine        int    `json:"src_line"`
	SrcCol         int    `json:"src_col"`
	SrcSymbolID    int64  `json:"src_symbol_id,omitempty"`
	SrcSymbolName  string `json:"src_symbol_name,omitempty"`
	DstName        string `json:"dst_name"`
	DstSymbolID    int64  `json:"dst_symbol_id,omitempty"`
	Kind           string `json:"kind"`
	Resolved       bool   `json:"resolved"`
}

// GetReferences returns use-sites for a symbol. The target can be specified
// by qualified name (preferred) or short name. project filters to a single
// workspace project when non-empty. pathsIn (v1.6) scopes the src file
// path via the --since filter.
func (r *Reader) GetReferences(ctx context.Context, target, project string, limit int, pathsIn []string) ([]ReferenceHit, error) {
	if limit <= 0 {
		limit = 100
	}
	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return nil, err
	}
	pathClause, pathArgs, err := pathsInClause(pathsIn)
	if err != nil {
		return nil, err
	}
	// Resolve target -> symbol ids (may be >1 for ambiguous short names).
	ids, err := r.symbolsByTarget(ctx, target)
	if err != nil {
		return nil, err
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
		args = append(args, limit)
		rows, err := r.db.QueryContext(ctx, `
			SELECT r.id, f.path, r.line, r.col,
			       COALESCE(r.src_symbol_id, 0), COALESCE(ss.qualified, ''),
			       r.dst_name, COALESCE(r.dst_symbol_id, 0), r.kind, r.resolved
			FROM refs r
			JOIN files f ON f.id = r.src_file_id
			LEFT JOIN symbols ss ON ss.id = r.src_symbol_id
			WHERE r.dst_symbol_id IN (`+placeholders+`)`+scope+pathClause+`
			ORDER BY f.path, r.line
			LIMIT ?`, args...)
		if err != nil {
			return nil, err
		}
		hits, err = scanReferenceHits(rows, hits)
		if err != nil {
			return nil, err
		}
	}

	// Textual-only fallback: refs unresolved but whose dst_name or dst_short
	// equals the target. Useful when the target isn't defined in this repo
	// (e.g. stdlib calls) or when resolution was ambiguous.
	remaining := limit - len(hits)
	if remaining > 0 {
		args := []any{target, target}
		args = append(args, scopeArgs...)
		args = append(args, pathArgs...)
		args = append(args, remaining)
		rows, err := r.db.QueryContext(ctx, `
			SELECT r.id, f.path, r.line, r.col,
			       COALESCE(r.src_symbol_id, 0), COALESCE(ss.qualified, ''),
			       r.dst_name, COALESCE(r.dst_symbol_id, 0), r.kind, r.resolved
			FROM refs r
			JOIN files f ON f.id = r.src_file_id
			LEFT JOIN symbols ss ON ss.id = r.src_symbol_id
			WHERE r.resolved = 0 AND (r.dst_name = ? OR r.dst_short = ?)`+scope+pathClause+`
			ORDER BY f.path, r.line
			LIMIT ?`, args...)
		if err != nil {
			return nil, err
		}
		hits, err = scanReferenceHits(rows, hits)
		if err != nil {
			return nil, err
		}
	}

	return hits, nil
}

func (r *Reader) symbolsByTarget(ctx context.Context, target string) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id FROM symbols WHERE qualified = ? OR name = ?`, target, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanReferenceHits(rows *sql.Rows, acc []ReferenceHit) ([]ReferenceHit, error) {
	defer rows.Close()
	for rows.Next() {
		var h ReferenceHit
		var resolved int
		if err := rows.Scan(&h.ID, &h.SrcPath, &h.SrcLine, &h.SrcCol,
			&h.SrcSymbolID, &h.SrcSymbolName, &h.DstName, &h.DstSymbolID,
			&h.Kind, &resolved); err != nil {
			return acc, err
		}
		h.Resolved = resolved == 1
		acc = append(acc, h)
	}
	return acc, rows.Err()
}

// FileHit represents a file in the index.
type FileHit struct {
	Path        string    `json:"path"`
	Language    string    `json:"language"`
	SymbolCount int       `json:"symbol_count"`
	SizeBytes   int64     `json:"size_bytes"`
	LastIndexed time.Time `json:"last_indexed"`
}

// ListFiles returns files matching an optional language filter and name
// substring. Globs can be layered by the CLI if richer matching is needed.
// pathsIn (v1.6) scopes to the --since path set.
func (r *Reader) ListFiles(ctx context.Context, language, nameContains, project string, limit int, pathsIn []string) ([]FileHit, error) {
	if limit <= 0 {
		limit = 500
	}
	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return nil, err
	}
	pathClause, pathArgs, err := pathsInClause(pathsIn)
	if err != nil {
		return nil, err
	}
	args := []interface{}{}
	conds := []string{}
	if language != "" {
		conds = append(conds, "f.language = ?")
		args = append(args, language)
	}
	if nameContains != "" {
		conds = append(conds, "f.path LIKE ?")
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
	args = append(args, limit)
	q := fmt.Sprintf(`
		SELECT f.path, f.language, f.size_bytes, f.last_indexed_at,
		       (SELECT COUNT(*) FROM symbols s WHERE s.file_id = f.id)
		FROM files f %s ORDER BY f.path LIMIT ?`, where)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileHit
	for rows.Next() {
		var h FileHit
		var ts int64
		if err := rows.Scan(&h.Path, &h.Language, &h.SizeBytes, &ts, &h.SymbolCount); err != nil {
			return nil, err
		}
		h.LastIndexed = time.Unix(ts, 0)
		out = append(out, h)
	}
	return out, rows.Err()
}

// FileOutlineItem is one entry in the hierarchical outline of a file.
type FileOutlineItem struct {
	SymbolID   int64             `json:"symbol_id"`
	Name       string            `json:"name"`
	Qualified  string            `json:"qualified"`
	Kind       string            `json:"kind"`
	StartLine  int               `json:"start_line"`
	EndLine    int               `json:"end_line"`
	Signature  string            `json:"signature,omitempty"`
	Children   []FileOutlineItem `json:"children,omitempty"`
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
		WHERE f.path = ?
		ORDER BY s.start_line`, path)
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

// Stats mirrors the write-side stats but lives here so all reads come from
// a single package.
// Stats exposes point-in-time counts + quality signals for the index.
// Quality signals (self_loop_count, unresolved_by_language, stale_chunks)
// were added in v1.1 so `myco doctor` and agents have honest numbers to
// judge the index by before running expensive tools.
type Stats struct {
	Files                int            `json:"files"`
	Symbols              int            `json:"symbols"`
	Refs                 int            `json:"refs"`
	Resolved             int            `json:"refs_resolved"`
	// v1.2 refs breakdown — captures the three honest states.
	// Invariant: Resolved + RefsExternalKnown + RefsTrulyUnresolved + <v0 short-name resolves>
	// = NonImportRefs. Import refs are counted separately.
	NonImportRefs        int            `json:"non_import_refs"`        // kind != 'import'
	RefsTypeResolved     int            `json:"refs_type_resolved"`     // resolver_version >= 1
	RefsExternalKnown    int            `json:"refs_external_known"`    // v1 + dst NULL (stdlib / deps)
	RefsTrulyUnresolved  int            `json:"refs_truly_unresolved"`  // v0 + dst NULL + kind != import
	SelfLoopCount        int            `json:"self_loop_count"`        // v0 self-loops (resolution bugs)
	RecursionSelfLoops   int            `json:"recursion_self_loops"`   // v>=1 self-loops (real recursion)
	UnresolvedByLanguage map[string]int `json:"unresolved_by_language"`
	TotalByLanguage      map[string]int `json:"total_refs_by_language"`
	Chunks               int            `json:"chunks"`
	ChunksEmbedded       int            `json:"chunks_embedded"`
	StaleChunks          int            `json:"stale_chunks"`
	EmbedQueueDepth      int            `json:"embed_queue_depth"`
	ByKind               map[string]int `json:"by_kind"`
	ByLang               map[string]int `json:"by_language"`
	DBSizeBytes          int64          `json:"db_size_bytes"`
	DBFreelistPages      int            `json:"db_freelist_pages"`
	DBPageCount          int            `json:"db_page_count"`
	// v2.1: interface-implementer linkage signal. Counts RefInherit edges
	// emitted by language resolvers and the distinct concrete types they
	// originate from. Surfaces via doctor as `interface_expansion_coverage`
	// so users can confirm the fan-out is actually populated.
	InterfaceImplementsRefs   int `json:"interface_implements_refs"`
	InterfaceConcreteTypes    int `json:"interface_concrete_types"`
	// v2.5 skills coverage: distinct package directories the index
	// knows about. The on-disk count comes from a filesystem walk in
	// the doctor layer (so missing files are caught even when the
	// skill_files DB row still exists). 0 when the user never ran
	// `myco skills compile`.
	SkillsPackagesIndexed int `json:"skills_packages_indexed"`
	// v3.1: configured workspace projects with per-project file counts.
	// Empty when no `projects:` block is set in `.mycelium.yml` (the
	// repo runs in single-project mode and `files.project_id` is NULL
	// everywhere). Used by doctor to flag misconfigured projects whose
	// include patterns matched nothing, and by FindSymbol's hint
	// generator to tell agents which project names are valid.
	ConfiguredProjects        []ProjectStats `json:"configured_projects,omitempty"`
	LastScan                  time.Time      `json:"last_scan"`
}

// ProjectStats is the per-project file count surfaced via Stats.
type ProjectStats struct {
	Name      string `json:"name"`
	Root      string `json:"root"`
	FileCount int    `json:"file_count"`
}

// UnresolvedRatio is the fraction of *non-import* refs that no resolver
// could place — neither the type-aware pass nor the short-name fallback.
// Type-resolved external refs (e.g. fmt.Println) don't count as unresolved
// even though their dst_symbol_id is NULL: we know exactly what they are,
// we just don't index the target package.
//
// Agents use this to decide whether to trust graph-traversal results.
// Lower is better; v1.2 target is <8% for Go.
func (s Stats) UnresolvedRatio() float64 {
	if s.NonImportRefs == 0 {
		return 0
	}
	return float64(s.RefsTrulyUnresolved) / float64(s.NonImportRefs)
}

// DBFragmentation is freelist_pages / page_count; a rough VACUUM signal.
func (s Stats) DBFragmentation() float64 {
	if s.DBPageCount == 0 {
		return 0
	}
	return float64(s.DBFreelistPages) / float64(s.DBPageCount)
}

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

	// Chunks + embed pipeline health. Stale = chunks that should have an
	// embedding (the active embedder isn't none) but don't. Computed here
	// as: total chunks - embedded chunks. The doctor layer reconciles with
	// the configured provider to decide Pass vs Warn.
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&s.Chunks)
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE embedding IS NOT NULL`).Scan(&s.ChunksEmbedded)
	s.StaleChunks = s.Chunks - s.ChunksEmbedded
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embed_queue`).Scan(&s.EmbedQueueDepth)

	// v2.1: interface-implementer linkage. RefInherit edges link concrete
	// types to interfaces they implement; agents querying upstream
	// consumers depend on these to reach interface-typed callers.
	_ = r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM refs WHERE kind = 'inherit'`,
	).Scan(&s.InterfaceImplementsRefs)
	_ = r.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT src_symbol_id) FROM refs WHERE kind = 'inherit'`,
	).Scan(&s.InterfaceConcreteTypes)

	// v2.5 skills coverage. SkillsPackagesIndexed = distinct
	// directories holding indexed files (same shape as
	// Compile.discoverPackages). On-disk count is computed by the
	// caller (doctor) via a filesystem walk so missing-file detection
	// works even when the skill_files DB row still exists.
	if pathRows, err := r.db.QueryContext(ctx, `SELECT path FROM files`); err == nil {
		dirs := map[string]struct{}{}
		for pathRows.Next() {
			var p string
			if err := pathRows.Scan(&p); err == nil {
				dirs[path.Dir(p)] = struct{}{}
			}
		}
		pathRows.Close()
		s.SkillsPackagesIndexed = len(dirs)
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
	return s, nil
}
