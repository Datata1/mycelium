// Integration test: exercises the full indexer against a committed
// multi-language fixture. No network. Runs in CI.
//
// Validates that each of the nine query tools returns something sensible
// for known-good inputs on a known-good repo. This is the backstop for
// refactors: if anyone breaks symbol extraction, ref resolution, or the
// query surface, this test fails.
package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		// v1.3 resolvers wired in so the test exercises the scope walkers.
		// Go resolver is skipped — the fixture has a bare main.go with no
		// go.mod so go/packages can't type-check it.
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
		// v4 P0 fix: type aliases / interfaces defined in .d.ts declaration
		// files must be findable via find_symbol.
		cases := []struct {
			name      string
			wantKind  string
			qualified string
		}{
			{"WorkspacePlan", "", "types.WorkspacePlan"},
			{"WorkspacePlanMap", "", "types.WorkspacePlanMap"},
			{"PlanSelector", "", "types.PlanSelector"},
			{"PlanTier", "", "types.PlanTier"},
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
			t.Error("Expanded outline should be populated even on small file")
		}
		if fr.Stats.ExpandedSymbols != fr.Stats.TotalSymbols {
			t.Errorf("ExpandedSymbols=%d, TotalSymbols=%d — preview should mark all symbols as expanded",
				fr.Stats.ExpandedSymbols, fr.Stats.TotalSymbols)
		}
	})

	t.Run("read_focused_no_focus_truncates_above_cap", func(t *testing.T) {
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
		if len(fr.Content) == 0 {
			t.Error("preview content is empty — should hold the first N lines")
		}
		if len(fr.Expanded) == 0 {
			t.Error("Expanded outline missing on truncated preview")
		}
	})

	t.Run("read_focused_with_focus_unchanged", func(t *testing.T) {
		fr, err := reader.ReadFocused(ctx, dst, "main.go", "Greeter")
		if err != nil {
			t.Fatalf("read_focused: %v", err)
		}
		if fr.Hint != "" {
			t.Errorf("focus set: Hint should be empty; got %q", fr.Hint)
		}
		if fr.Focus != "Greeter" {
			t.Errorf("Focus echo lost; got %q", fr.Focus)
		}
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
		s, err := reader.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if s.UnresolvedByLanguage["typescript"] != 0 {
			t.Errorf("typescript unresolved: got %d, want 0", s.UnresolvedByLanguage["typescript"])
		}
		if s.UnresolvedByLanguage["python"] != 0 {
			t.Errorf("python unresolved: got %d, want 0", s.UnresolvedByLanguage["python"])
		}
	})
}
