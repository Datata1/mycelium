package query

import (
	"context"
	"database/sql"
	"errors"
)

// GetFileSummary returns the summary for one file. An unindexed path is
// an error carrying "did you mean" suggestions and (when a probe is
// attached) the reason the file is absent — previously it returned a
// zero-value summary, indistinguishable from an empty file.
func (r *Reader) GetFileSummary(ctx context.Context, path string) (FileSummary, error) {
	s := FileSummary{Path: path, ByKind: map[string]int{}}

	var fileID int64
	var canonical string
	err := r.db.QueryRowContext(ctx,
		`SELECT f.id, f.language, COALESCE(p.name, ''), `+displayPath+`
		 FROM files f LEFT JOIN projects p ON p.id = f.project_id
		 WHERE f.path = ?
		    OR (p.root IS NOT NULL AND ? = p.root || '/' || f.path)`, path, path,
	).Scan(&fileID, &s.Language, &s.Project, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return s, notFound("file not in index: %s%s%s",
			path, formatPathSuggestions(suggestPaths(ctx, r.db, path, 3)),
			joinDiagnosis(r.diagnosePath(ctx, path)))
	}
	if err != nil {
		return s, err
	}
	// Echo the canonical repo-relative form regardless of input form.
	s.Path = canonical

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
