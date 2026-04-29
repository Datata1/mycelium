package query

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// LexicalHit is one matching line in a file.
type LexicalHit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

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
func (r *Reader) SearchLexical(ctx context.Context, pattern, pathContains, project string, k int, repoRoot string, pathsIn []string) ([]LexicalHit, error) {
	if k <= 0 {
		k = 50
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile pattern: %w", err)
	}
	paths, err := r.candidatePaths(ctx, pathContains, project, pathsIn)
	if err != nil {
		return nil, err
	}

	// Bounded worker pool. Four concurrent file reads is a good balance for
	// SSD-backed disks without starving the tree-sitter / parser workers that
	// run alongside on the daemon.
	const workers = 4
	jobs := make(chan candidatePath)
	hitsCh := make(chan LexicalHit, 64)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				abs := filepath.Join(repoRoot, j.projectRoot, j.rel)
				if err := scanFile(ctx, abs, j.rel, re, hitsCh); err != nil {
					// silent: skip unreadable files
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

	var hits []LexicalHit
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
	if ctx.Err() != nil {
		return hits, ctx.Err()
	}
	return hits, nil
}

// candidatePath pairs the index-stored path (project-relative in
// workspace mode) with the project root that needs to prefix it on
// disk. projectRoot is empty for files outside any explicit project
// (single-project mode where files.project_id is NULL).
type candidatePath struct {
	rel         string
	projectRoot string
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
	// participating with an empty projectRoot.
	q := `SELECT f.path, COALESCE(p.root, '')
	      FROM files f LEFT JOIN projects p ON p.id = f.project_id
	      WHERE 1=1`
	args := []any{}
	if pathContains != "" {
		q += ` AND f.path LIKE ?`
		args = append(args, "%"+pathContains+"%")
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
		if err := rs.Scan(&c.rel, &c.projectRoot); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rs.Err()
}

func scanFile(ctx context.Context, abs, rel string, re *regexp.Regexp, out chan<- LexicalHit) error {
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
			case out <- LexicalHit{Path: rel, Line: line, Snippet: strings.TrimRight(string(bytes.TrimRight(b, "\r")), " \t")}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return sc.Err()
}
