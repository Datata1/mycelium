package query

import (
	"context"
	"fmt"
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
	var hints []string

	// Project filter: did the named project exist at all?
	if project != "" {
		exists, err := r.projectExists(ctx, project)
		if err == nil && !exists {
			configured, _ := r.listProjectNames(ctx)
			if len(configured) == 0 {
				hints = append(hints, fmt.Sprintf(
					"no project named %q — this index has no `projects:` block configured (single-project mode); omit the project filter or add the project to .mycelium.yml",
					project))
			} else {
				hints = append(hints, fmt.Sprintf(
					"no project named %q — configured projects: %s",
					project, formatList(configured)))
			}
		}
	}

	// Kind filter: did anything match the name independent of kind, and
	// did the requested kind eliminate them? Or does the requested kind
	// not exist anywhere in this index?
	if kind != "" {
		// Re-run the name lookup with no kind filter to see what the
		// kind would have eliminated. Bounded by limit=20 so a wide
		// substring doesn't trigger an expensive scan on a misspelled
		// kind.
		matchedKinds, err := r.kindsForName(ctx, name)
		if err == nil {
			switch {
			case len(matchedKinds) > 0:
				// The name matches things, just not in the requested
				// kind. Most actionable hint we can give.
				hints = append(hints, fmt.Sprintf(
					"name %q matches symbols of kind %s, but kind=%q eliminated them — drop the kind filter or try one of those",
					name, formatList(matchedKinds), kind))
			default:
				// The name matches nothing regardless of kind. Help
				// the agent verify the kind value is valid at all.
				known, _ := r.knownKinds(ctx)
				if len(known) > 0 && !contains(known, kind) {
					hints = append(hints, fmt.Sprintf(
						"no symbols of kind %q in this repo — known kinds: %s",
						kind, formatList(known)))
				}
			}
		}
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

