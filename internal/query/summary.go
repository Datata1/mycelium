package query

import (
	"context"
	"database/sql"
)

// FileSummary is a structural summary of a single file. For v1.0 we stay
// firmly on the "derivable from the index" side — no LLM calls. Agents use
// this as a quick orientation before deciding whether to read the file.
type FileSummary struct {
	Path        string            `json:"path"`
	Language    string            `json:"language"`
	LOC         int               `json:"loc"`
	SymbolCount int               `json:"symbol_count"`
	ByKind      map[string]int    `json:"by_kind"`
	Exports     []ExportEntry     `json:"exports"`
	Imports     []string          `json:"imports"`
}

// ExportEntry is one publicly-visible symbol. We filter on visibility=public
// which covers Go capital-names, TS export_statement-wrapped defs, and Python
// non-underscore names.
type ExportEntry struct {
	Name      string `json:"name"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	StartLine int    `json:"start_line"`
	Signature string `json:"signature,omitempty"`
}

// GetFileSummary returns the summary for one file. If the file is not in the
// index, returns a FileSummary with Path set and other fields zero.
func (r *Reader) GetFileSummary(ctx context.Context, path string) (FileSummary, error) {
	s := FileSummary{Path: path, ByKind: map[string]int{}}

	var fileID int64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, language FROM files WHERE path = ?`, path,
	).Scan(&fileID, &s.Language)
	if err == sql.ErrNoRows {
		return s, nil
	}
	if err != nil {
		return s, err
	}

	// LOC = end_line of the last symbol, or 0. A full line count would mean
	// opening the file; the symbol-derived proxy is good enough for UX and
	// keeps this query DB-only.
	_ = r.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(end_line), 0) FROM symbols WHERE file_id = ?`, fileID,
	).Scan(&s.LOC)

	// Symbol counts.
	rows, err := r.db.QueryContext(ctx,
		`SELECT kind, COUNT(*) FROM symbols WHERE file_id = ? GROUP BY kind`, fileID)
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
		s.SymbolCount += n
	}
	rows.Close()

	// Exports: top-level public symbols (parent_id IS NULL excludes methods,
	// which are reachable via their class).
	rows, err = r.db.QueryContext(ctx, `
		SELECT name, qualified, kind, start_line, COALESCE(signature, '')
		FROM symbols
		WHERE file_id = ? AND visibility = 'public' AND parent_id IS NULL
		ORDER BY start_line`, fileID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var e ExportEntry
		if err := rows.Scan(&e.Name, &e.Qualified, &e.Kind, &e.StartLine, &e.Signature); err != nil {
			rows.Close()
			return s, err
		}
		s.Exports = append(s.Exports, e)
	}
	rows.Close()

	// Imports: refs of kind=import sourced from this file. Duplicates are
	// collapsed since the same module may be imported via multiple lines.
	rows, err = r.db.QueryContext(ctx, `
		SELECT DISTINCT dst_name FROM refs
		WHERE src_file_id = ? AND kind = 'import'
		ORDER BY dst_name`, fileID)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return s, err
		}
		s.Imports = append(s.Imports, name)
	}
	return s, rows.Err()
}
