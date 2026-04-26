package query

import (
	"context"
	"database/sql"
	"strings"
)

// AspectMatch is one symbol satisfying an aspect filter (e.g. "Go
// function whose signature returns error"). InboundRefs is computed
// via a correlated subquery so callers can rank by importance without
// a second round trip.
type AspectMatch struct {
	SymbolID    int64  `json:"symbol_id"`
	Name        string `json:"name"`
	Qualified   string `json:"qualified"`
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	Signature   string `json:"signature"`
	InboundRefs int    `json:"inbound_refs"`
}

// SymbolsBySignatureLike returns symbols whose signature matches ANY
// of the provided LIKE patterns, optionally restricted to a single
// language. Used by the "clean" aspect filters (error-handling,
// context-propagation) where a string match on the deterministic
// signature text is honest enough.
//
// Results are sorted by inbound ref count descending; ties broken by
// qualified name. limit caps the row count.
func (r *Reader) SymbolsBySignatureLike(ctx context.Context, language string, patterns []string, limit int) ([]AspectMatch, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	conds := make([]string, 0, len(patterns))
	args := make([]any, 0, len(patterns)+2)
	for _, p := range patterns {
		conds = append(conds, "s.signature LIKE ?")
		args = append(args, p)
	}
	where := "(" + strings.Join(conds, " OR ") + ")"
	if language != "" {
		where += " AND f.language = ?"
		args = append(args, language)
	}
	args = append(args, limit)
	q := `
		SELECT s.id, s.name, s.qualified, s.kind, f.path, s.start_line,
		       COALESCE(s.signature, ''),
		       (SELECT COUNT(*) FROM refs r WHERE r.dst_symbol_id = s.id) AS inbound
		FROM symbols s
		JOIN files f ON f.id = s.file_id
		WHERE ` + where + `
		ORDER BY inbound DESC, s.qualified ASC
		LIMIT ?`
	return scanAspectRows(r.db.QueryContext(ctx, q, args...))
}

// SymbolsByOutboundRef returns symbols that have at least one
// outbound ref matching either of two criteria:
//
//   - dstFilePrefix: ref's dst symbol lives in a file whose path
//     starts with this prefix (typical for in-repo aspects like
//     "anything that touches internal/config/").
//   - dstNameLike: ref's dst_name matches this LIKE pattern (typical
//     for stdlib hits like "log.%" where dst_symbol_id is NULL but
//     the textual target is preserved).
//
// Either argument may be empty to disable that branch; passing both
// empty returns nil. Results sorted by inbound ref count descending.
func (r *Reader) SymbolsByOutboundRef(ctx context.Context, language, dstFilePrefix, dstNameLike string, limit int) ([]AspectMatch, error) {
	if dstFilePrefix == "" && dstNameLike == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	conds := []string{}
	args := []any{}
	if dstFilePrefix != "" {
		conds = append(conds, `EXISTS (
			SELECT 1 FROM refs r
			JOIN symbols ds ON ds.id = r.dst_symbol_id
			JOIN files df ON df.id = ds.file_id
			WHERE r.src_symbol_id = s.id
			  AND r.kind != 'import'
			  AND df.path LIKE ? || '/%'
		)`)
		args = append(args, dstFilePrefix)
	}
	if dstNameLike != "" {
		conds = append(conds, `EXISTS (
			SELECT 1 FROM refs r
			WHERE r.src_symbol_id = s.id
			  AND r.kind != 'import'
			  AND r.dst_name LIKE ?
		)`)
		args = append(args, dstNameLike)
	}
	where := "(" + strings.Join(conds, " OR ") + ")"
	if language != "" {
		where += " AND f.language = ?"
		args = append(args, language)
	}
	args = append(args, limit)
	q := `
		SELECT s.id, s.name, s.qualified, s.kind, f.path, s.start_line,
		       COALESCE(s.signature, ''),
		       (SELECT COUNT(*) FROM refs r2 WHERE r2.dst_symbol_id = s.id) AS inbound
		FROM symbols s
		JOIN files f ON f.id = s.file_id
		WHERE ` + where + `
		ORDER BY inbound DESC, s.qualified ASC
		LIMIT ?`
	return scanAspectRows(r.db.QueryContext(ctx, q, args...))
}

func scanAspectRows(rows *sql.Rows, qErr error) ([]AspectMatch, error) {
	if qErr != nil {
		return nil, qErr
	}
	defer rows.Close()
	var out []AspectMatch
	for rows.Next() {
		var m AspectMatch
		if err := rows.Scan(&m.SymbolID, &m.Name, &m.Qualified, &m.Kind, &m.Path, &m.StartLine, &m.Signature, &m.InboundRefs); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
