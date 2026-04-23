-- v1.5: workspace mode.
--
-- Repos can declare multiple sub-projects with per-project config (root,
-- include, exclude, languages) in .mycelium.yml. When the `projects:` list
-- is empty, the repo is one implicit "root project" and project_id stays
-- NULL on every files row — backward-compatible with v1.4 indexes.
--
-- Cross-repo federation (N worktrees, one logical graph) is out of scope
-- for v2.0; see LIMITATIONS.md.

CREATE TABLE projects (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    root       TEXT    NOT NULL,       -- repo-relative, forward slashes
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_projects_root ON projects(root);

-- files.project_id: NULL = root project (no projects: list configured).
-- ON DELETE CASCADE drops files when a project is removed from config.
ALTER TABLE files ADD COLUMN project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE;
CREATE INDEX idx_files_project ON files(project_id);
