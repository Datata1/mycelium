package query

import (
	"context"
	"fmt"
)

// FileFreshnessRow is one sampled files row for the doctor freshness
// check: enough to reconstruct the absolute path and compare disk mtime
// against what indexing recorded.
type FileFreshnessRow struct {
	Path          string // project-relative, as stored in files.path
	ProjectRoot   string // "" for single-project rows
	MTimeNS       int64
	LastIndexedAt int64 // unix seconds
}

// SampleFiles returns up to n random files rows. Random rather than
// newest-first: staleness from lost watcher events is uniformly
// distributed, and a fixed ordering would keep re-checking the same
// corner of the tree.
func (r *Reader) SampleFiles(ctx context.Context, n int) ([]FileFreshnessRow, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT f.path, COALESCE(p.root, ''), f.mtime_ns, f.last_indexed_at
		FROM files f LEFT JOIN projects p ON p.id = f.project_id
		ORDER BY RANDOM() LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("sample files: %w", err)
	}
	defer rows.Close()
	var out []FileFreshnessRow
	for rows.Next() {
		var fr FileFreshnessRow
		if err := rows.Scan(&fr.Path, &fr.ProjectRoot, &fr.MTimeNS, &fr.LastIndexedAt); err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, rows.Err()
}

// AllFilePaths returns every files row as (projectRoot, path) pairs —
// the full index-side set for the doctor --deep walk diff. Cheap even
// on large repos: one indexed column scan, no joins beyond projects.
func (r *Reader) AllFilePaths(ctx context.Context) ([]FileFreshnessRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT f.path, COALESCE(p.root, ''), f.mtime_ns, f.last_indexed_at
		FROM files f LEFT JOIN projects p ON p.id = f.project_id
		ORDER BY f.path`)
	if err != nil {
		return nil, fmt.Errorf("all file paths: %w", err)
	}
	defer rows.Close()
	var out []FileFreshnessRow
	for rows.Next() {
		var fr FileFreshnessRow
		if err := rows.Scan(&fr.Path, &fr.ProjectRoot, &fr.MTimeNS, &fr.LastIndexedAt); err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, rows.Err()
}
