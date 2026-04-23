package index

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Project is one entry in the `projects` table. Exported so the pipeline
// can round-trip between config.ProjectConfig and the DB row.
type Project struct {
	ID   int64
	Name string
	Root string
}

// UpsertProject creates or updates a project by name. Returns the row ID.
// Idempotent — daemon restart calls this once per configured project and
// existing entries are unchanged when Root matches.
func (ix *Index) UpsertProject(ctx context.Context, name, root string) (int64, error) {
	var id int64
	err := ix.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE name = ?`, name).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		res, err := ix.db.ExecContext(ctx,
			`INSERT INTO projects(name, root, created_at) VALUES(?, ?, ?)`,
			name, root, time.Now().Unix())
		if err != nil {
			return 0, fmt.Errorf("insert project: %w", err)
		}
		id, _ := res.LastInsertId()
		return id, nil
	case err != nil:
		return 0, fmt.Errorf("select project: %w", err)
	}
	// Root drift means someone edited .mycelium.yml; keep the id, update
	// the stored root so later queries reflect the current layout.
	if _, err := ix.db.ExecContext(ctx, `UPDATE projects SET root = ? WHERE id = ?`, root, id); err != nil {
		return 0, fmt.Errorf("update project: %w", err)
	}
	return id, nil
}

// PruneProjects deletes project rows not in the keep set. Cascades drop
// files + symbols + refs + chunks owned by the removed project.
// Call on daemon start so removing a project from config actually cleans
// the index.
func (ix *Index) PruneProjects(ctx context.Context, keepIDs []int64) error {
	// Build a small in-memory set; sqlite IN with 100+ placeholders is
	// fine but we don't need to test that scale here.
	if len(keepIDs) == 0 {
		// Nothing to prune against; defensive no-op — the caller is
		// probably running in root-project-only mode (Projects empty).
		return nil
	}
	args := make([]any, len(keepIDs))
	placeholders := "?"
	for i, id := range keepIDs {
		args[i] = id
		if i > 0 {
			placeholders += ",?"
		}
	}
	q := fmt.Sprintf(`DELETE FROM projects WHERE id NOT IN (%s)`, placeholders)
	if _, err := ix.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("prune projects: %w", err)
	}
	return nil
}

// ListProjects returns all rows in the projects table. Used by `myco
// stats` / doctor for per-project breakdowns.
func (ix *Index) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT id, name, root FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Root); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
