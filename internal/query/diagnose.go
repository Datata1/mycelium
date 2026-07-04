package query

import (
	"context"
	"strings"
)

// diagnoseEmptyFind returns 0..N hint lines explaining why a FindSymbol
// call produced no matches. Callers only invoke it on the empty-result
// path, so the cost of the extra SQL it issues is paid by misses, not
// by the hot path.
//
// The hints are intentionally a flat []string of human-readable lines
// rather than a structured enum — the agent reading them is itself an
// LLM, and free-text reads naturally. Wording is unstable across v3.x;
// see CHANGELOG when it changes.
func (r *Reader) diagnoseEmptyFind(ctx context.Context, name, kind, project string) []string {
	in := findHintInput{name: name, kind: kind, project: project}

	// Fetches are gated so a miss doesn't pay for queries it can't use.
	if project != "" {
		if exists, err := r.projectExists(ctx, project); err == nil {
			in.projectExists = exists
			if !exists {
				in.configuredProjects, _ = r.listProjectNames(ctx)
			}
		} else {
			// Treat a lookup failure as "exists" so we emit no misleading
			// project hint — matches the pre-refactor behaviour.
			in.projectExists = true
		}
	}

	if kind != "" {
		// Bounded by limit=20 so a wide substring doesn't trigger an
		// expensive scan on a misspelled kind.
		if matched, err := r.kindsForName(ctx, name); err == nil {
			in.matchedKinds = matched
			if len(matched) == 0 {
				in.knownKinds, _ = r.knownKinds(ctx)
			}
		}
	}

	hints := buildFindHints(in)
	// A miss on an index that never completed a reconcile is more
	// likely staleness than a missing symbol — say so rather than let
	// the agent conclude "not in this repo" and fall back to grep.
	if _, ok, err := r.LastFullScanAt(ctx); err == nil && !ok {
		hints = append(hints, "index has never completed a full reconcile — run `myco index` or start `myco daemon`")
	}
	return hints
}

func (r *Reader) projectExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE name = ?`, name).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *Reader) listProjectNames(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT name FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, nil
}

// knownKinds is the set of distinct kinds this index actually contains.
// Lets the diagnose path reject typos like kind="function" when only
// "var" exists.
func (r *Reader) knownKinds(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT kind FROM symbols ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		kinds = append(kinds, k)
	}
	return kinds, nil
}

// kindsForName returns the distinct kinds of symbols that match `name`
// (substring on name or qualified name) ignoring any kind filter.
// Capped at 20 rows scanned to bound work on wide substrings.
func (r *Reader) kindsForName(ctx context.Context, name string) ([]string, error) {
	q := "%" + name + "%"
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT kind FROM symbols
		 WHERE (name LIKE ? OR qualified LIKE ?)
		 LIMIT 20`, q, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		kinds = append(kinds, k)
	}
	return kinds, nil
}

func formatList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	return "[" + strings.Join(items, ", ") + "]"
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
