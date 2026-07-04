package query

import (
	"fmt"
	"strings"
)

// MaxPathsIn caps the `--since <ref>` path list to avoid SQLite's 999-
// parameter limit on `path IN (?, ?, ...)`. A PR touching >500 files is
// almost always a sign the base ref is too old; callers should surface
// the error rather than truncate the filter and silently mislead.
const MaxPathsIn = 500

// displayPath is the repo-relative form of a stored file path, in SQL:
// workspace files get their project root prefixed, root-project files
// (project_id NULL) pass through unchanged. Every outward-facing SELECT
// emits this form so agents can hand any returned path straight to
// filesystem tools; the input side keeps accepting stored, repo-relative,
// and absolute forms (candidatePaths / the dual-path OR clauses).
// Requires `files` aliased as f and `projects` LEFT-JOINed as p — the
// convention every reader uses.
const displayPath = `CASE WHEN p.root IS NOT NULL AND p.root <> '' THEN p.root || '/' || f.path ELSE f.path END`

// displayPathJoin is the Go-side equivalent of displayPath for hits
// assembled outside SQL.
func displayPathJoin(projectRoot, stored string) string {
	if projectRoot == "" {
		return stored
	}
	return projectRoot + "/" + stored
}

// pathsInClause renders the shared `--since` path filter.
//
//   - nil         → ("", nil, nil)             unscoped
//   - empty slice → (" AND 1=0", nil, nil)     explicit zero-row sentinel
//   - non-empty   → (" AND <displayPath> IN (?,...)", args, nil)
//
// The filter values come from `git diff --name-only` and are therefore
// repo-relative; matching against displayPath (not the stored
// project-relative form) is what makes `--since` work in workspace mode.
//
// The splicer assumes the surrounding query already aliases `files` as
// `f` and LEFT-JOINs `projects` as `p`, matching the convention every
// other reader uses.
func pathsInClause(pathsIn []string) (string, []any, error) {
	if pathsIn == nil {
		return "", nil, nil
	}
	if len(pathsIn) == 0 {
		return " AND 1=0", nil, nil
	}
	if len(pathsIn) > MaxPathsIn {
		return "", nil, fmt.Errorf("since filter expanded to %d paths (max %d); pick a tighter base ref",
			len(pathsIn), MaxPathsIn)
	}
	placeholders := "?" + strings.Repeat(",?", len(pathsIn)-1)
	args := make([]any, 0, len(pathsIn))
	for _, p := range pathsIn {
		args = append(args, p)
	}
	return " AND " + displayPath + " IN (" + placeholders + ")", args, nil
}
