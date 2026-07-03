package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datata1/mycelium/internal/focus"
)

// FocusedRead is the result of ReadFocused: a single file's content
// rendered with focus-matched symbols expanded in full and non-matched
// symbols collapsed to a one-line marker. Stats let agents reason about
// how much was hidden and which line ranges to drill into next.
//
// `Hint` is populated only in the v4 no-focus preview path (focus=""):
// it tells the agent that Content is a truncated preview and how to get
// more (pass focus, or call get_file_outline). When focus is set, Hint
// is empty.
type FocusedRead struct {
	Path    string           `json:"path"`
	Focus   string           `json:"focus"`
	Content string           `json:"content"`
	Stats   FocusedReadStats `json:"stats"`
	Hint    string           `json:"hint,omitempty"`
	// Expanded reports each symbol that survived the filter, with its
	// original line range, so agents can map back to source for follow-ups.
	Expanded []FocusedSymbol `json:"expanded,omitempty"`
}

// ReadFocusedPreviewLines caps the verbatim line count returned in the
// no-focus preview path. Lifted to a package var (not a const) so the
// integration test can shrink it without needing a multi-hundred-line
// fixture file. v4 default is 50 — the bench against
// internal/telemetry/aggregate.go (12 KiB, ~280 lines) shows this drops
// the no-focus byte count from 14 KiB (heavier than Read) to ~5 KiB
// (genuinely lighter), closing the v3.4 A3 G2 net-negative case.
var ReadFocusedPreviewLines = 50

// FocusedReadStats summarises the collapse outcome.
type FocusedReadStats struct {
	TotalSymbols    int `json:"total_symbols"`
	ExpandedSymbols int `json:"expanded_symbols"`
	OriginalBytes   int `json:"original_bytes"`
	ReturnedBytes   int `json:"returned_bytes"`
}

// FocusedSymbol is a kept-after-filter symbol's location.
type FocusedSymbol struct {
	Qualified string  `json:"qualified"`
	Kind      string  `json:"kind"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
}

// flatSymbol is the symbol view ReadFocused walks: top-level + nested,
// flattened to a list ordered by start_line. Children are kept inline
// because collapsing a class hides its methods regardless of nesting.
type flatSymbol struct {
	Name      string
	Qualified string
	Kind      string
	StartLine int
	EndLine   int
	Signature string
	Docstring string
}

// ReadFocused returns the file at the path the index stores, with
// non-focus-matching symbols collapsed to one-line `// signature ...`
// markers.
//
// `path` must match what `list_files` / `find_symbol` returned — in
// workspace mode that's the **project-relative** path (e.g.
// `src/ci/ui-test.ts`), not the repo-relative one. The disk read joins
// the file's project root (looked up via `files.project_id`) with that
// path, so callers don't have to know the project layout.
//
// `repoRoot` is the absolute path to the repo. focusQ may be empty —
// in that case all symbols expand and the function effectively
// becomes a normal file read mediated by the daemon.
//
// The collapsed marker uses the language's line-comment syntax (// for
// Go/TS/JS, # for Python, // fallback otherwise). Line numbers in the
// returned content no longer correspond to source line numbers; the
// Expanded list maps survivors back.
func (r *Reader) ReadFocused(ctx context.Context, repoRoot, path, focusQ string) (FocusedRead, error) {
	out := FocusedRead{Path: path, Focus: focusQ}

	// Normalize absolute paths to repo-relative so the SQL match below
	// only needs to handle the two index-storage forms.
	lookup := path
	if filepath.IsAbs(lookup) && repoRoot != "" {
		if rel, err := filepath.Rel(repoRoot, lookup); err == nil && !strings.HasPrefix(rel, "..") {
			lookup = filepath.ToSlash(rel)
		}
	}

	var fileID int64
	var language string
	var dbPath string
	var projectRoot sql.NullString
	// In workspace mode files are stored with project-relative paths
	// (e.g. "src/utils/plans.ts"), but agents typically pass repo-relative
	// paths (e.g. "packages/ui-tests/src/utils/plans.ts"). The second OR
	// condition handles that case: it matches when the passed path equals
	// the project root joined with the stored path. f.path is selected
	// alongside so disk reconstruction below can use the canonical
	// project-relative form regardless of which form the caller passed.
	err := r.db.QueryRowContext(ctx,
		`SELECT f.id, f.language, f.path, p.root
		 FROM files f LEFT JOIN projects p ON p.id = f.project_id
		 WHERE f.path = ?
		    OR (p.root IS NOT NULL AND ? = p.root || '/' || f.path)`, lookup, lookup,
	).Scan(&fileID, &language, &dbPath, &projectRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return out, notFound("file not in index: %s%s",
			path, formatPathSuggestions(suggestPaths(ctx, r.db, path, 3)))
	}
	if err != nil {
		return out, err
	}

	// Reassemble the absolute disk path from the database row, not the
	// caller's input. `dbPath` is always project-relative (in workspace
	// mode) or repo-relative (single-project mode with NULL project_id),
	// so joining repo + project + dbPath is correct in both cases. Using
	// the input `path` here would double the project prefix when the
	// caller passed a repo-relative path that matched via the second OR
	// branch above.
	abs := path
	if !filepath.IsAbs(abs) {
		if projectRoot.Valid && projectRoot.String != "" {
			abs = filepath.Join(repoRoot, projectRoot.String, dbPath)
		} else {
			abs = filepath.Join(repoRoot, dbPath)
		}
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", abs, err)
	}
	out.Stats.OriginalBytes = len(raw)

	syms, err := r.flatSymbolsForFile(ctx, fileID)
	if err != nil {
		return out, err
	}
	out.Stats.TotalSymbols = len(syms)

	// v4 B1: no-focus preview path. Empty focus used to expand every
	// symbol — equivalent to a plain Read, plus the JSON envelope and
	// outline metadata. The v3.4 A3 bench measured this at 14 KiB on a
	// 12 KiB file, so myco was net-heavier than the agent's general-
	// purpose file reader. The fix surfaces an outline + first N lines +
	// a hint to pass focus, so the no-focus call shape is genuinely a
	// saving instead of an overhead.
	if focusQ == "" {
		return r.previewRead(out, raw, syms), nil
	}

	tokens := focus.Tokenize(focusQ)
	commentPrefix := lineCommentFor(language)

	marks := make([]markedSymbol, len(syms))
	for i, s := range syms {
		expand := true
		var score float64
		if len(tokens) > 0 {
			score, expand = focus.MatchTokens(tokens, focus.Candidate{
				Name:      s.Name,
				Qualified: s.Qualified,
				Docstring: s.Docstring,
			})
		}
		marks[i] = markedSymbol{flatSymbol: s, expand: expand, score: score}
		if expand {
			out.Stats.ExpandedSymbols++
			out.Expanded = append(out.Expanded, FocusedSymbol{
				Qualified: s.Qualified,
				Kind:      s.Kind,
				StartLine: s.StartLine,
				EndLine:   s.EndLine,
				Score:     score,
			})
		}
	}

	out.Content = renderCollapsed(string(raw), marks, commentPrefix)
	out.Stats.ReturnedBytes = len(out.Content)
	return out, nil
}

func (r *Reader) flatSymbolsForFile(ctx context.Context, fileID int64) ([]flatSymbol, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT name, qualified, kind, start_line, end_line,
		       COALESCE(signature, ''), COALESCE(docstring, '')
		FROM symbols
		WHERE file_id = ?
		ORDER BY start_line, end_line DESC`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []flatSymbol
	for rows.Next() {
		var s flatSymbol
		if err := rows.Scan(&s.Name, &s.Qualified, &s.Kind, &s.StartLine, &s.EndLine, &s.Signature, &s.Docstring); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// renderCollapsed walks the file line by line. For each marked symbol
// scheduled for collapse it replaces the [start_line, end_line] block
// with a single `<indent>// signature  // collapsed (lines N-M)` line.
// Lines outside any symbol pass through unchanged.
//
// Nested symbols (a method inside a collapsed class) are skipped during
// the walk because their parent's collapse already removed them.
type collapseRange struct {
	start, end int
	signature  string
}

type markedSymbol struct {
	flatSymbol
	expand bool
	score  float64
}

func renderCollapsed(content string, marks []markedSymbol, commentPrefix string) string {
	// Build a list of collapse ranges from non-expanded symbols. A
	// symbol whose range is fully nested inside another collapse range
	// is redundant (its parent already hides it).
	var collapses []collapseRange
	for _, m := range marks {
		if m.expand {
			continue
		}
		collapses = append(collapses, collapseRange{
			start:     m.StartLine,
			end:       m.EndLine,
			signature: oneLineSignature(m.Signature, m.Qualified, m.Kind),
		})
	}
	sort.Slice(collapses, func(i, j int) bool { return collapses[i].start < collapses[j].start })
	collapses = mergeNestedCollapses(collapses)

	lines := splitLinesPreserve(content)
	var out strings.Builder
	idx := 0
	for lineNo := 1; lineNo <= len(lines); lineNo++ {
		// Are we inside a collapse range? Find the first range that
		// starts at this line; emit its marker once and skip past end.
		if idx < len(collapses) && collapses[idx].start == lineNo {
			rg := collapses[idx]
			indent := leadingIndent(lines[rg.start-1])
			fmt.Fprintf(&out, "%s%s %s  %s collapsed (lines %d-%d)\n",
				indent, commentPrefix, rg.signature, commentPrefix, rg.start, rg.end)
			lineNo = rg.end
			idx++
			continue
		}
		// If we're inside a range that already started but somehow we
		// didn't catch it (e.g. nested edge case), skip silently.
		if idx < len(collapses) && lineNo > collapses[idx].end {
			idx++
			continue
		}
		out.WriteString(lines[lineNo-1])
	}
	return out.String()
}

// mergeNestedCollapses removes ranges fully contained inside earlier
// (outer) ranges so the renderer doesn't emit a marker for a hidden
// child. Assumes input is sorted by start ascending.
func mergeNestedCollapses(in []collapseRange) []collapseRange {
	if len(in) == 0 {
		return in
	}
	out := []collapseRange{in[0]}
	for _, c := range in[1:] {
		last := out[len(out)-1]
		if c.start >= last.start && c.end <= last.end {
			continue
		}
		out = append(out, c)
	}
	return out
}

// splitLinesPreserve returns lines INCLUDING their trailing newline so
// reassembly via WriteString is byte-exact for content that round-trips.
func splitLinesPreserve(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func leadingIndent(line string) string {
	for i, ch := range line {
		if ch != ' ' && ch != '\t' {
			return line[:i]
		}
	}
	return line
}

// oneLineSignature flattens a possibly-multi-line signature to a single
// line suitable for a collapse marker. Multi-line signatures (Go
// interface bodies, struct definitions) get truncated at the first
// newline with " ..." appended; very long single lines stay intact —
// the agent benefits more from the full identifier than from a
// premature ellipsis. Falls back to "<kind> <qualified>" when the
// signature is empty.
func oneLineSignature(signature, qualified, kind string) string {
	sig := strings.TrimSpace(signature)
	if sig == "" {
		if kind != "" && qualified != "" {
			return kind + " " + qualified
		}
		if qualified != "" {
			return qualified
		}
		return "(symbol)"
	}
	if i := strings.IndexByte(sig, '\n'); i >= 0 {
		sig = strings.TrimRight(sig[:i], " \t{") + " ..."
	}
	return sig
}

// previewRead builds the no-focus preview: outline (Expanded) + first
// ReadFocusedPreviewLines lines verbatim + a Hint pointing at focus= and
// get_file_outline. When the file is shorter than the cap, Content is
// the full file and Hint is empty (no point claiming "preview" when
// nothing was elided). Either way Expanded is populated so the agent
// gets the symbol map without a follow-up call.
func (r *Reader) previewRead(out FocusedRead, raw []byte, syms []flatSymbol) FocusedRead {
	for _, s := range syms {
		out.Expanded = append(out.Expanded, FocusedSymbol{
			Qualified: s.Qualified,
			Kind:      s.Kind,
			StartLine: s.StartLine,
			EndLine:   s.EndLine,
		})
	}
	out.Stats.ExpandedSymbols = len(syms)

	lines := splitLinesPreserve(string(raw))
	cut := ReadFocusedPreviewLines
	if cut <= 0 || len(lines) <= cut {
		out.Content = string(raw)
		out.Stats.ReturnedBytes = len(out.Content)
		return out
	}
	var b strings.Builder
	for i := 0; i < cut; i++ {
		b.WriteString(lines[i])
	}
	out.Content = b.String()
	out.Stats.ReturnedBytes = len(out.Content)
	out.Hint = fmt.Sprintf(
		"Preview only — first %d of %d lines shown. Pass `focus=<query>`%s to filter symbols and see the matching ones in full; or call `get_file_outline` for the symbol-only listing.",
		cut, len(lines), exampleFocusHint(syms),
	)
	return out
}

// exampleFocusHint picks a concrete identifier from the file so the
// hint shows the agent what a useful focus= value looks like, e.g.
// `focus="ComputeSessionCost"`. Falls back to a generic placeholder
// when the file has no symbols (rare — usually a doc-only file).
func exampleFocusHint(syms []flatSymbol) string {
	for _, s := range syms {
		if s.Name != "" {
			return fmt.Sprintf(" (e.g. focus=%q)", s.Name)
		}
	}
	return ""
}

func lineCommentFor(language string) string {
	switch strings.ToLower(language) {
	case "python", "py":
		return "#"
	case "go", "typescript", "javascript", "ts", "js", "tsx", "jsx":
		return "//"
	default:
		return "//"
	}
}
