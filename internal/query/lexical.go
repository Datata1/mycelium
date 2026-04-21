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
func (r *Reader) SearchLexical(ctx context.Context, pattern, pathContains string, k int, repoRoot string) ([]LexicalHit, error) {
	if k <= 0 {
		k = 50
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile pattern: %w", err)
	}
	paths, err := r.candidatePaths(ctx, pathContains)
	if err != nil {
		return nil, err
	}

	// Bounded worker pool. Four concurrent file reads is a good balance for
	// SSD-backed disks without starving the tree-sitter / parser workers that
	// run alongside on the daemon.
	const workers = 4
	jobs := make(chan string)
	hitsCh := make(chan LexicalHit, 64)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range jobs {
				if err := scanFile(ctx, filepath.Join(repoRoot, rel), rel, re, hitsCh); err != nil {
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

func (r *Reader) candidatePaths(ctx context.Context, pathContains string) ([]string, error) {
	var rows = func() (*[]string, error) {
		q := `SELECT path FROM files`
		args := []any{}
		if pathContains != "" {
			q += ` WHERE path LIKE ?`
			args = append(args, "%"+pathContains+"%")
		}
		rs, err := r.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		var out []string
		for rs.Next() {
			var p string
			if err := rs.Scan(&p); err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return &out, rs.Err()
	}
	out, err := rows()
	if err != nil {
		return nil, err
	}
	return *out, nil
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
