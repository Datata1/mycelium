// Package integration_test holds end-to-end integration tests for the full
// indexer + query stack. No network required. Runs in CI.
package integration_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/query"
)

// copyFixture copies testdata/fixtures/<name> into a fresh temp dir so tests
// can write to it without polluting the source tree.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		o, err := os.Create(out)
		if err != nil {
			return err
		}
		defer o.Close()
		_, err = io.Copy(o, in)
		return err
	}); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

func openIndex(t *testing.T, path string) *index.Index {
	t.Helper()
	ix, err := index.Open(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	return ix
}

func hasQualified(hits []query.SymbolHit, qualified string) bool {
	for _, h := range hits {
		if h.Qualified == qualified {
			return true
		}
	}
	return false
}

func names(hits []query.SymbolHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Qualified)
	}
	return out
}

func contains(ss []string, needle string) bool {
	for _, s := range ss {
		if s == needle {
			return true
		}
	}
	return false
}
