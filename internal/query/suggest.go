package query

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// pathSuggestion is one "did you mean" candidate returned from
// SuggestPaths. Project is the workspace project the file belongs to
// (empty in single-project mode); callers use it to render an
// unambiguous hint when the same basename exists in multiple projects.
type pathSuggestion struct {
	Path    string
	Project string
}

// suggestPaths returns up to limit indexed files whose basename matches
// the basename of input. Powers the "did you mean" tail on ReadFocused
// and SearchLexical errors when the caller's path/filter doesn't match
// anything in the index.
//
// The query uses a leading-wildcard LIKE which can't hit the f.path
// index, but on a ~10k-file index that scan completes in single-digit
// ms — fine for an error path that runs at most once per failed call.
// Returns nil on any DB error or empty result; callers treat nil as
// "no suggestions to add."
func suggestPaths(ctx context.Context, db *sql.DB, input string, limit int) []pathSuggestion {
	if limit <= 0 {
		limit = 3
	}
	base := filepath.Base(filepath.ToSlash(input))
	if base == "" || base == "." || base == "/" {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT `+displayPath+`, COALESCE(p.name, '')
		FROM files f LEFT JOIN projects p ON p.id = f.project_id
		WHERE f.path = ? OR f.path LIKE '%/' || ?
		ORDER BY 1
		LIMIT ?`, base, base, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []pathSuggestion
	for rows.Next() {
		var s pathSuggestion
		if err := rows.Scan(&s.Path, &s.Project); err != nil {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// formatPathSuggestions builds the "Did you mean" tail for an error
// message. Returns "" when there are no suggestions so callers can
// concatenate it unconditionally without leaking trailing whitespace
// or producing dangling headers when basename-match found nothing.
func formatPathSuggestions(in []pathSuggestion) string {
	if len(in) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nDid you mean:")
	for _, s := range in {
		if s.Project != "" {
			fmt.Fprintf(&sb, "\n  %s  (project: %s)", s.Path, s.Project)
		} else {
			fmt.Fprintf(&sb, "\n  %s", s.Path)
		}
	}
	return sb.String()
}
