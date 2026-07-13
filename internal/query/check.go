package query

import (
	"context"
	"fmt"
	"strings"
)

// FileSymbolRow is one symbol defined in a caller-supplied file set —
// the seed material for the verifier and for test selection.
type FileSymbolRow struct {
	ID        int64
	Name      string
	Qualified string
	Kind      string
	Path      string // repo-relative (displayPath)
}

// SymbolsInFiles returns every symbol whose file matches one of the
// given repo-relative paths. Shares pathsInClause semantics: nil = no
// filter is NOT supported here (a verifier always has a concrete
// scope), so nil/empty input returns an empty result.
func (r *Reader) SymbolsInFiles(ctx context.Context, paths []string) ([]FileSymbolRow, error) {
	if len(paths) == 0 {
		return []FileSymbolRow{}, nil
	}
	pathClause, pathArgs, err := pathsInClause(paths)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.name, s.qualified, s.kind, `+displayPath+`
		FROM symbols s JOIN files f ON f.id = s.file_id
		         LEFT JOIN projects p ON p.id = f.project_id
		WHERE 1=1`+pathClause+`
		ORDER BY s.qualified`, pathArgs...)
	if err != nil {
		return nil, fmt.Errorf("symbols in files: %w", err)
	}
	defer rows.Close()
	out := []FileSymbolRow{}
	for rows.Next() {
		var fs FileSymbolRow
		if err := rows.Scan(&fs.ID, &fs.Name, &fs.Qualified, &fs.Kind, &fs.Path); err != nil {
			return nil, err
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

// QualifiedExist reports which of the given qualified names are defined
// anywhere in the index. Chunked to stay under SQLite's parameter cap.
func (r *Reader) QualifiedExist(ctx context.Context, qualified []string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	const chunk = 500
	for start := 0; start < len(qualified); start += chunk {
		end := start + chunk
		if end > len(qualified) {
			end = len(qualified)
		}
		part := qualified[start:end]
		placeholders := "?" + strings.Repeat(",?", len(part)-1)
		args := make([]any, len(part))
		for i, q := range part {
			args[i] = q
		}
		rows, err := r.db.QueryContext(ctx,
			`SELECT DISTINCT qualified FROM symbols WHERE qualified IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("qualified exist: %w", err)
		}
		for rows.Next() {
			var q string
			if err := rows.Scan(&q); err != nil {
				rows.Close()
				return nil, err
			}
			out[q] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// RemovedName identifies a symbol that disappeared from the index: the
// old qualified form plus its short name for textual-fallback matching.
type RemovedName struct {
	Qualified string
	Name      string
}

// DanglingRef is one reference that still points at a removed symbol
// from a file outside the change set. Exact means the ref's dst_name
// equals the removed qualified name (high confidence — that call site
// is broken); non-exact matches went through dst_short and are only
// reported when the ref is not resolved to a still-existing symbol.
type DanglingRef struct {
	SrcPath      string
	SrcQualified string
	DstName      string
	DstShort     string
	Kind         string
	Line         int
	Exact        bool
}

// DanglingRefs finds references matching the removed names whose source
// file is NOT in changedPaths (edits inside the diff were intentional).
// Import refs are excluded — their dst names are module paths, and a
// module short name colliding with a removed symbol name would be noise.
func (r *Reader) DanglingRefs(ctx context.Context, removed []RemovedName, changedPaths []string) ([]DanglingRef, error) {
	if len(removed) == 0 {
		return []DanglingRef{}, nil
	}
	notChanged := ""
	changedArgs := []any{}
	if len(changedPaths) > 0 {
		if len(changedPaths) > MaxPathsIn {
			return nil, fmt.Errorf("dangling refs: %d changed paths exceeds %d", len(changedPaths), MaxPathsIn)
		}
		placeholders := "?" + strings.Repeat(",?", len(changedPaths)-1)
		notChanged = ` AND ` + displayPath + ` NOT IN (` + placeholders + `)`
		for _, p := range changedPaths {
			changedArgs = append(changedArgs, p)
		}
	}

	out := []DanglingRef{}
	seen := map[int64]struct{}{}
	// Chunk the removed set so params (chunk + changed) stay < 999.
	const chunk = 300
	for start := 0; start < len(removed); start += chunk {
		end := start + chunk
		if end > len(removed) {
			end = len(removed)
		}
		part := removed[start:end]

		quals := make([]any, len(part))
		shorts := make([]any, len(part))
		for i, rm := range part {
			quals[i] = rm.Qualified
			shorts[i] = rm.Name
		}
		placeholders := "?" + strings.Repeat(",?", len(part)-1)

		// Pass 1: exact dst_name matches — the qualified target is gone
		// index-wide, so these call sites are broken.
		exactQ := `
			SELECT r.id, ` + displayPath + `, COALESCE(ss.qualified, ''), r.dst_name, r.dst_short, r.kind, r.line
			FROM refs r
			JOIN files f ON f.id = r.src_file_id
			LEFT JOIN projects p ON p.id = f.project_id
			LEFT JOIN symbols ss ON ss.id = r.src_symbol_id
			WHERE r.kind != 'import' AND r.dst_name IN (` + placeholders + `)` + notChanged + `
			ORDER BY 2, r.line`
		args := append(append([]any{}, quals...), changedArgs...)
		if err := r.scanDanglers(ctx, exactQ, args, true, seen, &out); err != nil {
			return nil, err
		}

		// Pass 2: short-name matches with no live resolution target.
		// Textual-only evidence — short names collide, so these are
		// warn-grade. Refs resolved to a different, still-existing
		// symbol are excluded by the NOT-IN-symbols guard.
		shortQ := `
			SELECT r.id, ` + displayPath + `, COALESCE(ss.qualified, ''), r.dst_name, r.dst_short, r.kind, r.line
			FROM refs r
			JOIN files f ON f.id = r.src_file_id
			LEFT JOIN projects p ON p.id = f.project_id
			LEFT JOIN symbols ss ON ss.id = r.src_symbol_id
			WHERE r.kind != 'import' AND r.dst_short IN (` + placeholders + `)
			  AND (r.dst_symbol_id IS NULL OR r.dst_symbol_id NOT IN (SELECT id FROM symbols))` + notChanged + `
			ORDER BY 2, r.line`
		args = append(append([]any{}, shorts...), changedArgs...)
		if err := r.scanDanglers(ctx, shortQ, args, false, seen, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *Reader) scanDanglers(ctx context.Context, q string, args []any, exact bool, seen map[int64]struct{}, out *[]DanglingRef) error {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("dangling refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var d DanglingRef
		if err := rows.Scan(&id, &d.SrcPath, &d.SrcQualified, &d.DstName, &d.DstShort, &d.Kind, &d.Line); err != nil {
			return err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		d.Exact = exact
		*out = append(*out, d)
	}
	return rows.Err()
}

// FilesFreshness is the exact-scope sibling of SampleFiles: freshness
// rows for precisely the given repo-relative paths.
func (r *Reader) FilesFreshness(ctx context.Context, paths []string) ([]FileFreshnessRow, error) {
	if len(paths) == 0 {
		return []FileFreshnessRow{}, nil
	}
	pathClause, pathArgs, err := pathsInClause(paths)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT f.path, COALESCE(p.root, ''), f.mtime_ns, f.last_indexed_at
		FROM files f LEFT JOIN projects p ON p.id = f.project_id
		WHERE 1=1`+pathClause, pathArgs...)
	if err != nil {
		return nil, fmt.Errorf("files freshness: %w", err)
	}
	defer rows.Close()
	out := []FileFreshnessRow{}
	for rows.Next() {
		var fr FileFreshnessRow
		if err := rows.Scan(&fr.Path, &fr.ProjectRoot, &fr.MTimeNS, &fr.LastIndexedAt); err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, rows.Err()
}

// NamesExist reports which of the given short names are still carried
// by any symbol in the index — the disambiguation input for classifying
// bare textual references to removed symbols.
func (r *Reader) NamesExist(ctx context.Context, names []string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	const chunk = 500
	for start := 0; start < len(names); start += chunk {
		end := start + chunk
		if end > len(names) {
			end = len(names)
		}
		part := names[start:end]
		placeholders := "?" + strings.Repeat(",?", len(part)-1)
		args := make([]any, len(part))
		for i, n := range part {
			args[i] = n
		}
		rows, err := r.db.QueryContext(ctx,
			`SELECT DISTINCT name FROM symbols WHERE name IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("names exist: %w", err)
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return nil, err
			}
			out[n] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
