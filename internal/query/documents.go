package query

import (
	"context"
	"strings"
)

// DocumentHit is one (key, value, line) entry from the v3.3 documents
// surface. Project annotates the workspace project the entry's file
// belongs to ("" in single-project mode); Path follows the v3.1.2
// convention — pass it verbatim to `read_focused` / `get_file_outline`
// without prepending project roots.
type DocumentHit struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Project string `json:"project,omitempty"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	Line    int    `json:"line"`
}

// FindDocumentKey looks up entries whose key matches the supplied
// query. Match semantics:
//
//   - Exact equality on `key`
//   - Otherwise: substring (`%query%`) against `key`
//
// Ordering: exact match first, then prefix match, then substring,
// then by key length ascending. The intent mirrors FindSymbol's
// "best name match first" ordering so agents see the most likely
// hit at index 0.
//
// `kind` filters to one document kind (`i18n_json`,
// `package_json_deps`, `go_mod_requires`). `project` scopes by
// workspace project name. `limit` defaults to 50.
func (r *Reader) FindDocumentKey(
	ctx context.Context,
	keyQuery, kind, project string,
	limit int,
) ([]DocumentHit, error) {
	if limit <= 0 {
		limit = 50
	}
	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return nil, err
	}
	likePattern := "%" + keyQuery + "%"
	prefixPattern := keyQuery + "%"

	q := `
		SELECT d.id, d.kind, f.path, COALESCE(p.name, ''),
		       d.key, d.value, d.line
		FROM documents d
		JOIN files f ON f.id = d.file_id
		LEFT JOIN projects p ON p.id = f.project_id
		WHERE (d.key = ? OR d.key LIKE ?)`
	args := []any{keyQuery, likePattern}
	if kind != "" {
		q += ` AND d.kind = ?`
		args = append(args, kind)
	}
	if scope != "" {
		q += scope
		args = append(args, scopeArgs...)
	}
	q += `
		ORDER BY
		  CASE WHEN d.key = ? THEN 0
		       WHEN d.key LIKE ? THEN 1
		       ELSE 2 END,
		  length(d.key),
		  d.key
		LIMIT ?`
	args = append(args, keyQuery, prefixPattern, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DocumentHit{}
	for rows.Next() {
		var h DocumentHit
		if err := rows.Scan(&h.ID, &h.Kind, &h.Path, &h.Project, &h.Key, &h.Value, &h.Line); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// trimDocumentValue exists so callers can render preview lines without
// leaking unbounded JSON values into terminal output. Kept here so the
// CLI and MCP path can share it without re-implementing.
func trimDocumentValue(v string, max int) string {
	if max <= 0 || len(v) <= max {
		return v
	}
	return strings.TrimRight(v[:max], " \t") + "…"
}

// HasDocumentFiles reports whether at least one row in `files` carries
// a non-NULL document_kind. Used by doctor to decide whether to emit
// the `documents_indexed` check at all — on a code-only repo we don't
// want to print an empty payload.
func (r *Reader) HasDocumentFiles(ctx context.Context) bool {
	var n int
	_ = r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM files WHERE document_kind IS NOT NULL`,
	).Scan(&n)
	return n > 0
}

// EmptyDocumentFiles returns up to `limit` paths of files registered
// with a document_kind but whose `documents` join is empty — the
// symptom of a parser that claimed a file and produced zero entries
// (config error or unexpected file shape). Doctor surfaces these as
// a hint.
func (r *Reader) EmptyDocumentFiles(ctx context.Context, limit int) []string {
	if limit <= 0 {
		limit = 5
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT f.path FROM files f
		LEFT JOIN documents d ON d.file_id = f.id
		WHERE f.document_kind IS NOT NULL AND d.id IS NULL
		LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			out = append(out, p)
		}
	}
	return out
}
