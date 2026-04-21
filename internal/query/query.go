package query

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
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

// FindSymbol returns symbols matching name (substring match for v0.2). FTS5
// trigram lookup lands with the switch from LIKE to MATCH once we validate
// the tokenizer across platforms.
func (r *Reader) FindSymbol(ctx context.Context, name, kind string, limit int) ([]SymbolHit, error) {
	if limit <= 0 {
		limit = 20
	}
	q := "%" + name + "%"
	var (
		rows *sql.Rows
		err  error
	)
	if kind == "" {
		rows, err = r.db.QueryContext(ctx, `
			SELECT s.id, s.name, s.qualified, s.kind, f.path, s.start_line, s.end_line,
			       COALESCE(s.signature, ''), COALESCE(s.docstring, '')
			FROM symbols s JOIN files f ON f.id = s.file_id
			WHERE s.name LIKE ? OR s.qualified LIKE ?
			ORDER BY
			  CASE WHEN s.name = ? THEN 0
			       WHEN s.name LIKE ? THEN 1 ELSE 2 END,
			  length(s.qualified)
			LIMIT ?`,
			q, q, name, name+"%", limit)
	} else {
		rows, err = r.db.QueryContext(ctx, `
			SELECT s.id, s.name, s.qualified, s.kind, f.path, s.start_line, s.end_line,
			       COALESCE(s.signature, ''), COALESCE(s.docstring, '')
			FROM symbols s JOIN files f ON f.id = s.file_id
			WHERE (s.name LIKE ? OR s.qualified LIKE ?) AND s.kind = ?
			ORDER BY length(s.qualified)
			LIMIT ?`,
			q, q, kind, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []SymbolHit
	for rows.Next() {
		var h SymbolHit
		if err := rows.Scan(&h.ID, &h.Name, &h.Qualified, &h.Kind, &h.Path, &h.StartLine, &h.EndLine, &h.Signature, &h.Docstring); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
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
// by qualified name (preferred) or short name.
func (r *Reader) GetReferences(ctx context.Context, target string, limit int) ([]ReferenceHit, error) {
	if limit <= 0 {
		limit = 100
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
		args := make([]interface{}, 0, len(ids)+1)
		for _, id := range ids {
			args = append(args, id)
		}
		args = append(args, limit)
		rows, err := r.db.QueryContext(ctx, `
			SELECT r.id, f.path, r.line, r.col,
			       COALESCE(r.src_symbol_id, 0), COALESCE(ss.qualified, ''),
			       r.dst_name, COALESCE(r.dst_symbol_id, 0), r.kind, r.resolved
			FROM refs r
			JOIN files f ON f.id = r.src_file_id
			LEFT JOIN symbols ss ON ss.id = r.src_symbol_id
			WHERE r.dst_symbol_id IN (`+placeholders+`)
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
		rows, err := r.db.QueryContext(ctx, `
			SELECT r.id, f.path, r.line, r.col,
			       COALESCE(r.src_symbol_id, 0), COALESCE(ss.qualified, ''),
			       r.dst_name, COALESCE(r.dst_symbol_id, 0), r.kind, r.resolved
			FROM refs r
			JOIN files f ON f.id = r.src_file_id
			LEFT JOIN symbols ss ON ss.id = r.src_symbol_id
			WHERE r.resolved = 0 AND (r.dst_name = ? OR r.dst_short = ?)
			ORDER BY f.path, r.line
			LIMIT ?`, target, target, remaining)
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
func (r *Reader) ListFiles(ctx context.Context, language, nameContains string, limit int) ([]FileHit, error) {
	if limit <= 0 {
		limit = 500
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
func (r *Reader) GetFileOutline(ctx context.Context, path string) ([]FileOutlineItem, error) {
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
	return out, nil
}

// Stats mirrors the write-side stats but lives here so all reads come from
// a single package.
type Stats struct {
	Files    int            `json:"files"`
	Symbols  int            `json:"symbols"`
	Refs     int            `json:"refs"`
	Resolved int            `json:"refs_resolved"`
	ByKind   map[string]int `json:"by_kind"`
	ByLang   map[string]int `json:"by_language"`
	LastScan time.Time      `json:"last_scan"`
}

func (r *Reader) Stats(ctx context.Context) (Stats, error) {
	s := Stats{ByKind: map[string]int{}, ByLang: map[string]int{}}
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
	rows, err := r.db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM symbols GROUP BY kind`)
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
