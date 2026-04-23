// Integration test: exercises the full indexer against a committed
// multi-language fixture. No network. Runs in CI.
//
// Validates that each of the nine query tools returns something sensible
// for known-good inputs on a known-good repo. This is the backstop for
// refactors: if anyone breaks symbol extraction, ref resolution, or the
// query surface, this test fails.
package mycelium_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/parser/python"
	"github.com/jdwiederstein/mycelium/internal/parser/typescript"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
	pyresolver "github.com/jdwiederstein/mycelium/internal/resolver/python"
	tsresolver "github.com/jdwiederstein/mycelium/internal/resolver/typescript"
)

func TestIntegration_IndexAndQuery(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, "testdata/fixtures/sample")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	defer ix.Close()

	reg := parser.NewRegistry()
	reg.Register(golang.New())
	reg.Register(typescript.New())
	reg.Register(python.New())

	walker := repo.NewWalker(
		dst,
		[]string{"**/*.go", "src/**/*.ts", "py/**/*.py"},
		nil,
		0,
	)
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   walker,
		Embedder: embed.Noop{},
		// v1.3 resolvers wired in so the test exercises the scope
		// walkers. Go resolver is skipped — the fixture has a bare
		// main.go with no go.mod so `go/packages` can't type-check it.
		Resolvers: map[string]pipeline.Resolver{
			"typescript": tsresolver.New(),
			"python":     pyresolver.New(),
		},
	}
	rep, err := p.RunOnce(ctx)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if rep.FilesChanged == 0 {
		t.Fatalf("no files changed on first index: %+v", rep)
	}

	reader := query.NewReader(ix.DB())

	t.Run("stats", func(t *testing.T) {
		s, err := reader.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if s.Files != 3 {
			t.Errorf("files: got %d, want 3", s.Files)
		}
		if s.Symbols < 8 {
			t.Errorf("symbols: got %d, want >=8", s.Symbols)
		}
		for _, lang := range []string{"go", "typescript", "python"} {
			if s.ByLang[lang] == 0 {
				t.Errorf("expected files for language %s, got 0", lang)
			}
		}
	})

	t.Run("find_symbol", func(t *testing.T) {
		hits, err := reader.FindSymbol(ctx, "AuthService", "", "", 10)
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if !hasQualified(hits, "auth.AuthService") {
			t.Errorf("expected auth.AuthService; got %+v", names(hits))
		}
	})

	t.Run("get_references_go", func(t *testing.T) {
		hits, err := reader.GetReferences(ctx, "NewGreeter", "", 20)
		if err != nil {
			t.Fatalf("get_references: %v", err)
		}
		if len(hits) == 0 {
			t.Fatalf("expected at least one reference to NewGreeter")
		}
		var resolved bool
		for _, h := range hits {
			if h.Resolved {
				resolved = true
				break
			}
		}
		if !resolved {
			t.Errorf("expected at least one resolved ref to NewGreeter")
		}
	})

	t.Run("list_files", func(t *testing.T) {
		files, err := reader.ListFiles(ctx, "", "", "", 100)
		if err != nil {
			t.Fatalf("list_files: %v", err)
		}
		if len(files) != 3 {
			t.Errorf("files: got %d, want 3", len(files))
		}
	})

	t.Run("get_file_outline", func(t *testing.T) {
		out, err := reader.GetFileOutline(ctx, "main.go")
		if err != nil {
			t.Fatalf("outline: %v", err)
		}
		var foundGreeter bool
		for _, it := range out {
			if it.Name == "Greeter" && len(it.Children) == 0 {
				// The Greeter type has no parent-linked methods in the fixture
				// because parent wiring is within-file only (a Go method's
				// receiver type is at the same file).
			}
			if it.Qualified == "main.Greeter" {
				foundGreeter = true
			}
		}
		if !foundGreeter {
			t.Errorf("outline of main.go missing main.Greeter")
		}
	})

	t.Run("get_file_summary", func(t *testing.T) {
		s, err := reader.GetFileSummary(ctx, "main.go")
		if err != nil {
			t.Fatalf("summary: %v", err)
		}
		if s.Language != "go" {
			t.Errorf("summary.Language = %q, want go", s.Language)
		}
		if !contains(s.Imports, "fmt") {
			t.Errorf("imports missing fmt: %v", s.Imports)
		}
	})

	t.Run("get_neighborhood_inbound", func(t *testing.T) {
		nb, err := reader.GetNeighborhood(ctx, "NewGreeter", "", 2, query.DirIn)
		if err != nil {
			t.Fatalf("neighborhood: %v", err)
		}
		if nb.Seed.Qualified != "main.NewGreeter" {
			t.Errorf("seed = %q, want main.NewGreeter", nb.Seed.Qualified)
		}
		if len(nb.Edges) == 0 {
			t.Errorf("expected at least one inbound edge to NewGreeter")
		}
	})

	t.Run("search_lexical", func(t *testing.T) {
		hits, err := reader.SearchLexical(ctx, `Hola`, "", "", 10, dst)
		if err != nil {
			t.Fatalf("lexical: %v", err)
		}
		if len(hits) == 0 {
			t.Fatalf("expected hit for 'Hola'")
		}
		if !strings.Contains(hits[0].Path, "main.go") {
			t.Errorf("expected hit in main.go; got %s", hits[0].Path)
		}
	})

	t.Run("v1.3_ts_this_method_resolution", func(t *testing.T) {
		// issueToken calls this.fingerprint — TS scope walker should
		// resolve the call to auth.AuthService.fingerprint.
		hits, err := reader.GetReferences(ctx, "auth.AuthService.fingerprint", "", 10)
		if err != nil {
			t.Fatalf("refs: %v", err)
		}
		found := false
		for _, h := range hits {
			if h.Resolved && strings.Contains(h.SrcPath, "auth.ts") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected resolved this.fingerprint call from auth.ts; got %+v", hits)
		}
	})

	t.Run("v1.3_python_self_method_resolution", func(t *testing.T) {
		// JobQueue.drain calls self.dequeue — Python scope walker should
		// resolve the call to worker.JobQueue.dequeue.
		hits, err := reader.GetReferences(ctx, "worker.JobQueue.dequeue", "", 10)
		if err != nil {
			t.Fatalf("refs: %v", err)
		}
		found := false
		for _, h := range hits {
			if h.Resolved && strings.Contains(h.SrcPath, "worker.py") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected resolved self.dequeue call from worker.py; got %+v", hits)
		}
	})

	t.Run("v1.3_no_truly_unresolved_refs", func(t *testing.T) {
		// With all three resolvers wired (minus Go, which needs a go.mod
		// this fixture doesn't have), truly-unresolved should be zero
		// across TS + Python — every call the resolver visits stamps
		// ResolverVersion, even when it can't rewrite the name.
		s, err := reader.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		// Inspect per-language to ignore the Go fixture's isolated main.go.
		if s.UnresolvedByLanguage["typescript"] != 0 {
			t.Errorf("typescript unresolved: got %d, want 0", s.UnresolvedByLanguage["typescript"])
		}
		if s.UnresolvedByLanguage["python"] != 0 {
			t.Errorf("python unresolved: got %d, want 0", s.UnresolvedByLanguage["python"])
		}
	})

	t.Run("search_semantic_requires_embedder", func(t *testing.T) {
		s := &query.Searcher{Reader: reader, Embedder: embed.Noop{}}
		_, err := s.SearchSemantic(ctx, "any query", 5, "", "", "")
		if err != embed.ErrNotConfigured {
			t.Errorf("expected ErrNotConfigured, got %v", err)
		}
	})
}

// copyFixture copies testdata/fixtures/<name> into a fresh temp dir so the
// test can write to it without polluting the source tree.
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
