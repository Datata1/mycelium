package query

import (
	"context"
	"database/sql"
)

// projectScope resolves an optional project name to a SQL clause + args
// that every scoped query can splice in. Returns ("", nil, nil) when the
// caller passed "" (meaning "all projects"). Returns a clause of the form
// " AND f.project_id = ?" plus the resolved id when a project matches.
// Returns a sentinel clause that matches nothing when the name doesn't
// exist, so callers don't accidentally see cross-project data.
func (r *Reader) projectScope(ctx context.Context, name string) (string, []any, error) {
	if name == "" {
		return "", nil, nil
	}
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE name = ?`, name).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		// Force zero results instead of silently dropping the filter.
		return " AND 1=0", nil, nil
	case err != nil:
		return "", nil, err
	}
	return " AND f.project_id = ?", []any{id}, nil
}
