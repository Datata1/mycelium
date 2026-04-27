package index

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SkillFile is the v2.5 hash record for one rendered file in the
// skills tree (one of: packages/<dir>/SKILL.md, aspects/<name>/
// INDEX.md, INDEX.md). The hash is over the rendered bytes so
// cross-package ref-count drift naturally invalidates without a
// separate dependency graph.
type SkillFile struct {
	Path        string // forward-slash, tree-relative
	SkillHash   string
	GeneratedAt time.Time
}

// SkillFileHash returns the stored hash for a file, or empty string
// when the file has no record yet (first-render case).
func (ix *Index) SkillFileHash(ctx context.Context, path string) (string, error) {
	var h string
	err := ix.db.QueryRowContext(ctx,
		`SELECT skill_hash FROM skill_files WHERE path = ?`, path).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("select skill_file: %w", err)
	}
	return h, nil
}

// UpsertSkillFile writes (path, hash, now) into skill_files.
// Idempotent — same hash is a no-op except for the timestamp, which
// we deliberately bump so doctor can show a freshness signal.
func (ix *Index) UpsertSkillFile(ctx context.Context, path, hash string) error {
	_, err := ix.db.ExecContext(ctx, `
		INSERT INTO skill_files(path, skill_hash, generated_at) VALUES(?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
		    skill_hash   = excluded.skill_hash,
		    generated_at = excluded.generated_at`,
		path, hash, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("upsert skill_file: %w", err)
	}
	return nil
}

// PruneSkillFiles deletes rows whose path is not in keep. Used at
// the end of a full Compile run to drop entries for removed
// packages or retired aspects.
func (ix *Index) PruneSkillFiles(ctx context.Context, keep []string) error {
	if len(keep) == 0 {
		// Defensive: refuse to nuke the table when caller passed
		// nothing. A real "all gone" case routes through DeleteSkillFile.
		return nil
	}
	placeholders := strings.Repeat("?,", len(keep))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(keep))
	for i, p := range keep {
		args[i] = p
	}
	q := fmt.Sprintf(`DELETE FROM skill_files WHERE path NOT IN (%s)`, placeholders)
	if _, err := ix.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("prune skill_files: %w", err)
	}
	return nil
}

// DeleteSkillFile removes one row. Used by the incremental path when
// a package's last source file is removed.
func (ix *Index) DeleteSkillFile(ctx context.Context, path string) error {
	_, err := ix.db.ExecContext(ctx, `DELETE FROM skill_files WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("delete skill_file: %w", err)
	}
	return nil
}

// ListSkillFiles returns every row, ordered by path. Used by doctor
// to compute the on-disk vs indexed coverage ratio.
func (ix *Index) ListSkillFiles(ctx context.Context) ([]SkillFile, error) {
	rows, err := ix.db.QueryContext(ctx,
		`SELECT path, skill_hash, generated_at FROM skill_files ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SkillFile
	for rows.Next() {
		var s SkillFile
		var ts int64
		if err := rows.Scan(&s.Path, &s.SkillHash, &ts); err != nil {
			return nil, err
		}
		s.GeneratedAt = time.Unix(ts, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}
