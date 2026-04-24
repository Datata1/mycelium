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

// pathsInClause renders the shared `--since` path filter.
//
//   - nil         → ("", nil, nil)             unscoped
//   - empty slice → (" AND 1=0", nil, nil)     explicit zero-row sentinel
//   - non-empty   → (" AND f.path IN (?,...)", args, nil)
//
// The splicer assumes the surrounding query already aliases `files` as
// `f`, matching the convention every other reader uses.
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
	return " AND f.path IN (" + placeholders + ")", args, nil
}
