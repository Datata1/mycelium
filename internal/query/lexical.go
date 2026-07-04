package query

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// SearchLexical does a ripgrep-style scan of every indexed file. Pattern is
// treated as a Go regexp; callers who want a plain substring can escape it
// with regexp.QuoteMeta before calling.
//
// This scans files on disk rather than indexing content up-front, which keeps
// the DB small. On a repo with ~1k files we easily finish in tens of ms with
// parallel reads.
//
// pathsIn (v1.6) is the `--since` path filter. nil = unscoped; empty =
// zero hits (no candidate files).
//
// Empty Matches carry Hints when the reader can explain or redirect:
// identifier-shaped patterns get a find_symbol pointer, and candidate
// files missing on disk surface as a staleness warning instead of the
// previous stderr-only log.
func (r *Reader) SearchLexical(ctx context.Context, pattern, pathContains, project string, k int, repoRoot string, pathsIn []string) (SearchLexicalResult, error) {
	res := SearchLexicalResult{Matches: []LexicalHit{}}
	if k <= 0 {
		k = 50
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// v4 T3: regex compile errors get an actionable hint —
		// agents typing `WorkspacePlan|plans` should know whether the
		// engine takes Go regexp syntax (it does — `|` is alternation,
		// `.` is any-char, etc.) versus a literal-string surface.
		return res, fmt.Errorf("compile pattern %q: %w (Go regexp syntax: |, ., [], (?i), etc. all supported; use regexp.QuoteMeta-style escaping for literal strings)", pattern, err)
	}
	paths, err := r.candidatePaths(ctx, pathContains, project, pathsIn)
	if err != nil {
		return res, err
	}
	// When the caller's path_contains filter eliminates every indexed
	// file, the worker pool would silently return zero hits and the
	// agent can't distinguish that from "pattern not found in matching
	// files." Surface it as an explicit error with basename-match
	// suggestions so the agent can correct the filter on the next call.
	if pathContains != "" && len(paths) == 0 {
		diag := ""
		if lines := r.diagnosePath(ctx, pathContains); len(lines) > 0 {
			diag = "\n" + strings.Join(lines, "\n")
		}
		return res, fmt.Errorf("no indexed files match path_contains=%q (try a different substring or omit the filter)%s%s",
			pathContains, formatPathSuggestions(suggestPaths(ctx, r.db, pathContains, 3)), diag)
	}

	// Bounded worker pool. Four concurrent file reads is a good balance for
	// SSD-backed disks without starving the tree-sitter / parser workers that
	// run alongside on the daemon.
	const workers = 4
	jobs := make(chan candidatePath)
	hitsCh := make(chan LexicalHit, 64)
	var wg sync.WaitGroup
	var missingOnDisk atomic.Int64

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				abs := filepath.Join(repoRoot, j.projectRoot, j.rel)
				if err := scanFile(ctx, abs, j.rel, j.projectName, re, hitsCh); err != nil {
					// ENOENT means the index is stale or the path
					// reconstruction is wrong — both are things the
					// caller should know about, not silently empty
					// results. Counted into the result hints; also
					// logged so daemon logs keep the exact path.
					if errors.Is(err, fs.ErrNotExist) {
						missingOnDisk.Add(1)
						fmt.Fprintf(os.Stderr,
							"[search_lexical] file in index but not on disk: %s\n", abs)
					}
					continue
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, p := range paths {
			select {
			case <-ctx.Done():
				return
			case jobs <- p:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(hitsCh)
	}()

	hits := []LexicalHit{}
	for h := range hitsCh {
		hits = append(hits, h)
		if len(hits) >= k {
			// Signal workers to stop. We drain the remaining channel so the
			// goroutines can finish cleanly.
			for range hitsCh {
			}
			break
		}
	}
	res.Matches = hits
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	// v4 T3: log a daemon-side hint when path_contains narrowed the
	// candidate set but no hits surfaced. Kept alongside the wire hints
	// so daemon logs still tell the story.
	if pathContains != "" && len(hits) == 0 {
		fmt.Fprintf(os.Stderr,
			"[search_lexical] 0 hits for pattern=%q in %d files matching path_contains=%q — try widening the path filter or simplifying the pattern\n",
			pattern, len(paths), pathContains)
	}
	if len(hits) == 0 {
		res.Hints = buildLexicalHints(pattern, identifierShaped(pattern), int(missingOnDisk.Load()))
	}
	return res, nil
}

// identifierShapedRE matches patterns that look like a symbol name
// (possibly dotted/qualified) rather than a regex or literal phrase.
var identifierShapedRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

func identifierShaped(pattern string) bool { return identifierShapedRE.MatchString(pattern) }

// candidatePath pairs the index-stored path (project-relative in
// workspace mode) with the project root that needs to prefix it on
// disk. projectRoot is empty for files outside any explicit project
// (single-project mode where files.project_id is NULL).
//
// projectName (v3.1.2+) is carried through to LexicalHit so callers
// can disambiguate hits across workspace projects.
type candidatePath struct {
	rel         string
	projectRoot string
	projectName string
}

func (r *Reader) candidatePaths(ctx context.Context, pathContains, project string, pathsIn []string) ([]candidatePath, error) {
	scope, scopeArgs, err := r.projectScope(ctx, project)
	if err != nil {
		return nil, err
	}
	pathClause, pathArgs, err := pathsInClause(pathsIn)
	if err != nil {
		return nil, err
	}
	// candidatePaths builds its own SELECT so the scope clause needs a
	// valid WHERE anchor; start with 1=1 and AND everything in. LEFT
	// JOIN to projects so single-project (NULL project_id) files keep
	// participating with an empty projectRoot/projectName.
	q := `SELECT f.path, COALESCE(p.root, ''), COALESCE(p.name, '')
	      FROM files f LEFT JOIN projects p ON p.id = f.project_id
	      WHERE 1=1`
	args := []any{}
	if pathContains != "" {
		// Match either the stored project-relative path or its
		// repo-relative form (`p.root || '/' || f.path`). This mirrors
		// the path-resolution OR pattern in ReadFocused so agents can
		// pass either form as a filter substring in workspace mode.
		q += ` AND (f.path LIKE ?
		         OR (p.root IS NOT NULL AND (p.root || '/' || f.path) LIKE ?))`
		args = append(args, "%"+pathContains+"%", "%"+pathContains+"%")
	}
	if scope != "" {
		q += scope
		args = append(args, scopeArgs...)
	}
	if pathClause != "" {
		q += pathClause
		args = append(args, pathArgs...)
	}
	rs, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []candidatePath
	for rs.Next() {
		var c candidatePath
		if err := rs.Scan(&c.rel, &c.projectRoot, &c.projectName); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rs.Err()
}

func scanFile(ctx context.Context, abs, rel, project string, re *regexp.Regexp, out chan<- LexicalHit) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b := sc.Bytes()
		if re.Match(b) {
			select {
			case out <- LexicalHit{Path: rel, Project: project, Line: line, Snippet: strings.TrimRight(string(bytes.TrimRight(b, "\r")), " \t")}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return sc.Err()
}
