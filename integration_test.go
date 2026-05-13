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
		// 4 files: main.go + auth.ts + py + types.d.ts (v4 .d.ts fixture).
		if s.Files != 4 {
			t.Errorf("files: got %d, want 4", s.Files)
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

	t.Run("find_symbol_in_d_ts_v4_T1_fix", func(t *testing.T) {
		// v4 P0 fix for the F1/T1 finding: type aliases / interfaces
		// defined in `.d.ts` declaration files must be findable via
		// find_symbol. The fixture testdata/fixtures/sample/src/types.d.ts
		// mirrors the monorepo-4 shape that returned null. Each shape
		// (interface, type alias, enum) gets its own assertion so a
		// regression on any one fails loudly with the right name.
		cases := []struct {
			name      string
			wantKind  string // empty = don't check kind
			qualified string
		}{
			{"WorkspacePlan", "", "types.WorkspacePlan"}, // interface
			{"WorkspacePlanMap", "", "types.WorkspacePlanMap"}, // type alias
			{"PlanSelector", "", "types.PlanSelector"}, // type alias (object)
			{"PlanTier", "", "types.PlanTier"}, // enum
		}
		for _, tc := range cases {
			res, err := reader.FindSymbol(ctx, tc.name, "", "", 10, nil, "")
			if err != nil {
				t.Errorf("find_symbol(%q): %v", tc.name, err)
				continue
			}
			if !hasQualified(res.Matches, tc.qualified) {
				t.Errorf("%s: expected %q in matches; got %v",
					tc.name, tc.qualified, names(res.Matches))
			}
		}
	})

	t.Run("find_symbol", func(t *testing.T) {
		res, err := reader.FindSymbol(ctx, "AuthService", "", "", 10, nil, "")
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if !hasQualified(res.Matches, "auth.AuthService") {
			t.Errorf("expected auth.AuthService; got %+v", names(res.Matches))
		}
		if len(res.Hints) != 0 {
			t.Errorf("expected no hints on a successful match; got %v", res.Hints)
		}
	})

	t.Run("find_symbol_hints_unknown_project", func(t *testing.T) {
		res, err := reader.FindSymbol(ctx, "AuthService", "", "no-such-project", 10, nil, "")
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if len(res.Matches) != 0 {
			t.Fatalf("expected zero matches under bogus project; got %d", len(res.Matches))
		}
		if len(res.Hints) == 0 {
			t.Fatalf("expected at least one hint about the bogus project filter")
		}
		joined := strings.Join(res.Hints, "\n")
		if !strings.Contains(joined, "no-such-project") {
			t.Errorf("expected hint to mention the bogus project name; got %q", joined)
		}
	})

	t.Run("find_symbol_hints_kind_eliminated", func(t *testing.T) {
		// AuthService is a struct/type, so kind=function should eliminate
		// it. The hint should tell us which kind(s) actually matched.
		res, err := reader.FindSymbol(ctx, "AuthService", "function", "", 10, nil, "")
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if len(res.Matches) != 0 {
			t.Fatalf("expected zero matches under kind=function; got %d", len(res.Matches))
		}
		if len(res.Hints) == 0 {
			t.Fatalf("expected at least one hint about the kind filter")
		}
	})

	t.Run("find_symbol_no_hints_on_real_miss", func(t *testing.T) {
		// Genuinely-not-present name with no filters: empty Matches,
		// empty Hints (we have nothing useful to say).
		res, err := reader.FindSymbol(ctx, "TotallyMadeUpSymbolName_xyzzy", "", "", 10, nil, "")
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if len(res.Matches) != 0 {
			t.Fatalf("expected zero matches; got %d", len(res.Matches))
		}
		if len(res.Hints) != 0 {
			t.Errorf("expected no hints on a real miss; got %v", res.Hints)
		}
	})

	t.Run("get_references_go", func(t *testing.T) {
		hits, err := reader.GetReferences(ctx, "NewGreeter", "", 20, nil)
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
		files, err := reader.ListFiles(ctx, "", "", "", 100, nil)
		if err != nil {
			t.Fatalf("list_files: %v", err)
		}
		// 4 files: main.go + auth.ts + py + types.d.ts (v4 .d.ts fixture).
		if len(files) != 4 {
			t.Errorf("files: got %d, want 4", len(files))
		}
	})

	t.Run("get_file_outline", func(t *testing.T) {
		out, err := reader.GetFileOutline(ctx, "main.go", "")
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

	t.Run("read_focused_no_focus_returns_outline_only_envelope", func(t *testing.T) {
		// v4 B1: empty focus used to return the full file plus the outline
		// metadata (heavier than a plain Read). main.go in the fixture is
		// 35 lines — smaller than the default preview cap (50) — so the
		// preview path returns the full content but still WITHOUT a Hint
		// (no truncation happened). Expanded must still be populated so the
		// agent gets the symbol map without a follow-up call.
		fr, err := reader.ReadFocused(ctx, dst, "main.go", "")
		if err != nil {
			t.Fatalf("read_focused: %v", err)
		}
		if fr.Hint != "" {
			t.Errorf("small file: Hint should be empty (no truncation); got %q", fr.Hint)
		}
		if fr.Stats.ReturnedBytes != fr.Stats.OriginalBytes {
			t.Errorf("small file: ReturnedBytes=%d, OriginalBytes=%d — expected equal (no truncation)",
				fr.Stats.ReturnedBytes, fr.Stats.OriginalBytes)
		}
		if len(fr.Expanded) == 0 {
			t.Error("Expanded outline should be populated even on small file (no-focus path always emits the symbol map)")
		}
		if fr.Stats.ExpandedSymbols != fr.Stats.TotalSymbols {
			t.Errorf("ExpandedSymbols=%d, TotalSymbols=%d — preview should mark all symbols as expanded for outline purposes",
				fr.Stats.ExpandedSymbols, fr.Stats.TotalSymbols)
		}
	})

	t.Run("read_focused_no_focus_truncates_above_cap", func(t *testing.T) {
		// Shrink the preview cap below the fixture file size so the
		// truncation branch fires. main.go has ~35 lines; cap=10 forces
		// the cut. Restore the package var after the test so other cases
		// (and the live binary) keep the production default.
		origCap := query.ReadFocusedPreviewLines
		query.ReadFocusedPreviewLines = 10
		defer func() { query.ReadFocusedPreviewLines = origCap }()

		fr, err := reader.ReadFocused(ctx, dst, "main.go", "")
		if err != nil {
			t.Fatalf("read_focused: %v", err)
		}
		if fr.Hint == "" {
			t.Error("truncated file: Hint should be set; got empty")
		}
		if !strings.Contains(fr.Hint, "Preview only") {
			t.Errorf("Hint should mention 'Preview only'; got %q", fr.Hint)
		}
		if !strings.Contains(fr.Hint, "focus=") {
			t.Errorf("Hint should suggest passing focus=; got %q", fr.Hint)
		}
		if fr.Stats.ReturnedBytes >= fr.Stats.OriginalBytes {
			t.Errorf("ReturnedBytes=%d should be < OriginalBytes=%d after truncation",
				fr.Stats.ReturnedBytes, fr.Stats.OriginalBytes)
		}
		// Sanity: returned content is the prefix of the source.
		if len(fr.Content) == 0 {
			t.Error("preview content is empty — should hold the first N lines")
		}
		if len(fr.Expanded) == 0 {
			t.Error("Expanded outline missing on truncated preview — agent loses the symbol map")
		}
	})

	t.Run("read_focused_with_focus_unchanged", func(t *testing.T) {
		// The non-empty-focus path is the v3.x behaviour and must be
		// untouched by the v4 B1 preview branch: Hint stays empty,
		// content carries collapse markers for non-matching symbols.
		fr, err := reader.ReadFocused(ctx, dst, "main.go", "Greeter")
		if err != nil {
			t.Fatalf("read_focused: %v", err)
		}
		if fr.Hint != "" {
			t.Errorf("focus set: Hint should be empty (preview is no-focus only); got %q", fr.Hint)
		}
		if fr.Focus != "Greeter" {
			t.Errorf("Focus echo lost; got %q", fr.Focus)
		}
		// Content carries the collapse markers when at least one symbol
		// didn't match. The fixture has main + NewGreeter + Greeter; with
		// focus="Greeter", at least main should collapse.
		if !strings.Contains(fr.Content, "collapsed (lines") {
			t.Errorf("expected collapse markers in focused content; got: %s", fr.Content)
		}
	})

	t.Run("get_neighborhood_inbound", func(t *testing.T) {
		nb, err := reader.GetNeighborhood(ctx, "NewGreeter", "", 2, query.DirIn, "")
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
		hits, err := reader.SearchLexical(ctx, `Hola`, "", "", 10, dst, nil)
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
		hits, err := reader.GetReferences(ctx, "auth.AuthService.fingerprint", "", 10, nil)
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
		hits, err := reader.GetReferences(ctx, "worker.JobQueue.dequeue", "", 10, nil)
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
		_, err := s.SearchSemantic(ctx, "any query", 5, "", "", "", nil)
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
