// Integration test for v1.5 workspace mode. Exercises a 3-project
// fixture (Go api, TS web, Python worker) end-to-end: indexing tags
// files with their project_id, and queries correctly scope by project
// name.
package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/parser/python"
	"github.com/datata1/mycelium/internal/parser/typescript"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
)

func TestIntegration_WorkspaceMode(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, "testdata/fixtures/workspace")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	defer ix.Close()

	reg := parser.NewRegistry()
	reg.Register(golang.New())
	reg.Register(typescript.New())
	reg.Register(python.New())

	projects := []struct {
		name    string
		root    string
		include []string
	}{
		{"api", "services/api", []string{"**/*.go"}},
		{"web", "services/web", []string{"**/*.ts"}},
		{"worker", "services/worker", []string{"**/*.py"}},
	}
	projectIDs := map[string]int64{}
	var workspaces []pipeline.Workspace
	for _, p := range projects {
		id, err := ix.UpsertProject(ctx, p.name, p.root)
		if err != nil {
			t.Fatalf("upsert %s: %v", p.name, err)
		}
		projectIDs[p.name] = id
		w := repo.NewWalker(filepath.Join(dst, p.root), p.include, nil, 0)
		workspaces = append(workspaces, pipeline.Workspace{ProjectID: id, Walker: w})
	}

	p := &pipeline.Pipeline{
		Index:      ix,
		Registry:   reg,
		Workspaces: workspaces,
	}
	rep, err := p.RunOnce(ctx)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if rep.FilesChanged != 3 {
		t.Fatalf("files changed: got %d, want 3 (one per project)", rep.FilesChanged)
	}

	reader := query.NewReader(ix.DB())

	t.Run("find_symbol_scopes_by_project", func(t *testing.T) {
		cases := []struct {
			project string
			want    int
		}{
			{"api", 1},
			{"web", 0},
			{"worker", 0},
			{"", 1}, // unscoped still finds it
		}
		for _, tc := range cases {
			res, err := reader.FindSymbol(ctx, "APIOnlySymbol", "", tc.project, 10, nil, "")
			if err != nil {
				t.Fatalf("find_symbol project=%q: %v", tc.project, err)
			}
			if len(res.Matches) != tc.want {
				t.Errorf("find_symbol APIOnlySymbol project=%q: got %d hits, want %d",
					tc.project, len(res.Matches), tc.want)
			}
		}
	})

	t.Run("list_files_scopes_by_project", func(t *testing.T) {
		cases := []struct {
			project string
			want    int
		}{
			{"api", 1},
			{"web", 1},
			{"worker", 1},
			{"", 3},
		}
		for _, tc := range cases {
			lfRes, err := reader.ListFiles(ctx, "", "", tc.project, 100, nil)
			files := lfRes.Matches
			if err != nil {
				t.Fatalf("list_files project=%q: %v", tc.project, err)
			}
			if len(files) != tc.want {
				t.Errorf("list_files project=%q: got %d files, want %d",
					tc.project, len(files), tc.want)
			}
		}
	})

	t.Run("unknown_project_returns_zero_not_unscoped", func(t *testing.T) {
		// A typo'd project name must not silently fall back to an
		// unscoped query — that would mask config bugs.
		res, err := reader.FindSymbol(ctx, "APIOnlySymbol", "", "does-not-exist", 10, nil, "")
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if len(res.Matches) != 0 {
			t.Errorf("unknown project should yield 0 hits, got %d", len(res.Matches))
		}
		// v3.1: empty result with unknown project should carry a hint.
		if len(res.Hints) == 0 {
			t.Error("expected at least one hint for unknown project; got none")
		}
	})

	// v3.1.1 regression: ReadFocused and SearchLexical both join repoRoot
	// with the index-stored path, but in workspace mode that path is
	// project-relative — the disk file lives at repoRoot+projectRoot+path.
	// Pre-fix, both tools silently failed (ReadFocused with an error,
	// SearchLexical by swallowing the read error and returning empty).
	t.Run("read_focused_resolves_workspace_path", func(t *testing.T) {
		rf, err := reader.ReadFocused(ctx, dst, "server.go", "")
		if err != nil {
			t.Fatalf("ReadFocused: %v", err)
		}
		if rf.Stats.OriginalBytes == 0 {
			t.Fatalf("expected non-empty read; got 0 bytes")
		}
		if !strings.Contains(rf.Content, "APIOnlySymbol") {
			t.Errorf("expected file content to contain APIOnlySymbol; got %q", rf.Content)
		}
	})

	// v3.1.2: ReadFocused must accept all three path forms agents commonly
	// produce — project-relative (from find_symbol), repo-relative (from
	// the user's own `cd` context), and absolute (from explicit
	// constructions). The pre-fix version doubled the project prefix when
	// fed repo-relative paths and produced ENOENT.
	t.Run("read_focused_accepts_repo_relative_path", func(t *testing.T) {
		rf, err := reader.ReadFocused(ctx, dst, "services/api/server.go", "")
		if err != nil {
			t.Fatalf("ReadFocused (repo-relative): %v", err)
		}
		if rf.Stats.OriginalBytes == 0 {
			t.Fatalf("expected non-empty read; got 0 bytes")
		}
		if !strings.Contains(rf.Content, "APIOnlySymbol") {
			t.Errorf("expected APIOnlySymbol in content; got %q", rf.Content)
		}
	})

	t.Run("read_focused_accepts_absolute_path", func(t *testing.T) {
		abs := filepath.Join(dst, "services/api/server.go")
		rf, err := reader.ReadFocused(ctx, dst, abs, "")
		if err != nil {
			t.Fatalf("ReadFocused (absolute): %v", err)
		}
		if rf.Stats.OriginalBytes == 0 {
			t.Fatalf("expected non-empty read; got 0 bytes")
		}
	})

	t.Run("search_lexical_reads_workspace_files", func(t *testing.T) {
		res, err := reader.SearchLexical(ctx, "APIOnlySymbol", "", "", 10, dst, nil)
		if err != nil {
			t.Fatalf("SearchLexical: %v", err)
		}
		hits := res.Matches
		if len(hits) == 0 {
			t.Fatalf("expected at least one APIOnlySymbol match; got 0 (likely the silent-skip path bug)")
		}
		// Paths are emitted repo-relative so agents can hand them
		// straight to filesystem tools.
		var found bool
		for _, h := range hits {
			if h.Path == "services/api/server.go" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected hit at path 'services/api/server.go'; got %+v", hits)
		}
	})

	// v3.1.2: path_contains must match both project-relative and
	// repo-relative substrings. Pre-fix, only project-relative matched
	// (filter against f.path only), so an agent narrowing a search to
	// "services/api" got zero hits even though server.go was indexed.
	t.Run("search_lexical_path_contains_accepts_repo_relative", func(t *testing.T) {
		res, err := reader.SearchLexical(ctx, "APIOnlySymbol", "services/api", "", 10, dst, nil)
		if err != nil {
			t.Fatalf("SearchLexical with repo-relative path_contains: %v", err)
		}
		hits := res.Matches
		if len(hits) == 0 {
			t.Errorf("expected hits when filtering by repo-relative substring; got 0")
		}
	})

	t.Run("search_lexical_path_contains_accepts_project_relative", func(t *testing.T) {
		res, err := reader.SearchLexical(ctx, "APIOnlySymbol", "server.go", "", 10, dst, nil)
		if err != nil {
			t.Fatalf("SearchLexical with project-relative path_contains: %v", err)
		}
		hits := res.Matches
		if len(hits) == 0 {
			t.Errorf("expected hits when filtering by project-relative substring; got 0")
		}
	})

	// v3.1.2: every path-bearing result type carries a Project annotation
	// so agents can disambiguate when the same path exists in multiple
	// workspace projects. omitempty drops it when project is "" so
	// single-project users see no JSON shape change.
	t.Run("symbol_hit_carries_project", func(t *testing.T) {
		res, err := reader.FindSymbol(ctx, "APIOnlySymbol", "", "", 10, nil, "")
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if len(res.Matches) == 0 {
			t.Fatalf("expected at least one hit")
		}
		if res.Matches[0].Project != "api" {
			t.Errorf("SymbolHit.Project: got %q, want %q", res.Matches[0].Project, "api")
		}
	})

	t.Run("file_hit_carries_project", func(t *testing.T) {
		lfRes, err := reader.ListFiles(ctx, "", "", "", 100, nil)
		files := lfRes.Matches
		if err != nil {
			t.Fatalf("list_files: %v", err)
		}
		got := map[string]string{}
		for _, f := range files {
			got[f.Path] = f.Project
		}
		want := map[string]string{
			"services/api/server.go":  "api",
			"services/web/src/app.ts": "web",
			"services/worker/job.py":  "worker",
		}
		for p, proj := range want {
			if got[p] != proj {
				t.Errorf("FileHit(%q).Project: got %q, want %q", p, got[p], proj)
			}
		}
	})

	t.Run("file_summary_carries_project", func(t *testing.T) {
		s, err := reader.GetFileSummary(ctx, "server.go")
		if err != nil {
			t.Fatalf("GetFileSummary: %v", err)
		}
		if s.Project != "api" {
			t.Errorf("FileSummary.Project: got %q, want %q", s.Project, "api")
		}
	})

	t.Run("lexical_hit_carries_project", func(t *testing.T) {
		res, err := reader.SearchLexical(ctx, "APIOnlySymbol", "", "", 10, dst, nil)
		if err != nil {
			t.Fatalf("SearchLexical: %v", err)
		}
		hits := res.Matches
		if len(hits) == 0 {
			t.Fatalf("expected at least one hit")
		}
		if hits[0].Project != "api" {
			t.Errorf("LexicalHit.Project: got %q, want %q", hits[0].Project, "api")
		}
	})

	// v3.1.2: helpful "Did you mean" hints for paths that don't resolve.
	// ReadFocused runs a basename match against the index and appends up
	// to 3 suggestions to its ENOENT error. SearchLexical does the same
	// when path_contains filters to zero candidate files (previously a
	// silent empty result).
	t.Run("read_focused_typo_includes_suggestion", func(t *testing.T) {
		// "servr.go" is a typo of "server.go" — basename match should
		// surface it.
		_, err := reader.ReadFocused(ctx, dst, "servr.go", "")
		if err == nil {
			t.Fatalf("expected error for typo'd path")
		}
		msg := err.Error()
		if !strings.Contains(msg, "file not in index") {
			t.Errorf("expected 'file not in index' headline; got %q", msg)
		}
		// Basename "servr.go" has no real near-match in the fixture, so
		// no Did-you-mean tail. Re-test below with a real typo case.
		if strings.Contains(msg, "Did you mean") {
			t.Errorf("unexpected suggestion tail for non-matching typo: %q", msg)
		}
	})

	t.Run("read_focused_typo_with_near_match_suggests", func(t *testing.T) {
		// Pass a wrong directory but a real filename — basename match
		// should find server.go and suggest it with project annotation.
		_, err := reader.ReadFocused(ctx, dst, "wrong/dir/server.go", "")
		if err == nil {
			t.Fatalf("expected error for non-existent path")
		}
		msg := err.Error()
		if !strings.Contains(msg, "Did you mean") {
			t.Fatalf("expected 'Did you mean' tail; got %q", msg)
		}
		if !strings.Contains(msg, "server.go") {
			t.Errorf("expected suggestion to include 'server.go'; got %q", msg)
		}
		if !strings.Contains(msg, "project: api") {
			t.Errorf("expected suggestion to include 'project: api'; got %q", msg)
		}
	})

	t.Run("search_lexical_zero_candidates_errors_with_suggestion", func(t *testing.T) {
		// path_contains filters to a substring that matches no indexed
		// files — basename match on "no-such-dir" finds nothing, but
		// the error itself must surface so the agent doesn't read the
		// empty result as "pattern not present in matching files."
		_, err := reader.SearchLexical(ctx, "anything", "no-such-dir", "", 10, dst, nil)
		if err == nil {
			t.Fatalf("expected error for path_contains with zero matches")
		}
		if !strings.Contains(err.Error(), `no indexed files match path_contains="no-such-dir"`) {
			t.Errorf("expected 'no indexed files match' headline; got %q", err.Error())
		}
	})

	t.Run("search_lexical_zero_candidates_with_near_match_suggests", func(t *testing.T) {
		// "srver.go" basename doesn't match anything, but
		// path_contains="server.go" (matching the real file) is the
		// success case — verify the typo path produces a suggestion.
		_, err := reader.SearchLexical(ctx, "anything", "wrong/server.go", "", 10, dst, nil)
		if err == nil {
			t.Fatalf("expected error for typo'd path_contains")
		}
		msg := err.Error()
		if !strings.Contains(msg, "Did you mean") {
			t.Fatalf("expected suggestion tail for near-match typo; got %q", msg)
		}
		if !strings.Contains(msg, "server.go") {
			t.Errorf("expected 'server.go' in suggestion; got %q", msg)
		}
	})

	// v3.1.2 regression-pin: GetFileSummary and GetFileOutline are DB-only
	// (no disk read), so the path-doubling bug doesn't affect them. But
	// they share the same OR-match pattern in SQL — pin both path forms
	// here so future refactors can't regress workspace-mode coverage.
	t.Run("get_file_summary_accepts_both_path_forms", func(t *testing.T) {
		for _, p := range []string{"server.go", "services/api/server.go"} {
			s, err := reader.GetFileSummary(ctx, p)
			if err != nil {
				t.Fatalf("GetFileSummary(%q): %v", p, err)
			}
			if s.SymbolCount == 0 {
				t.Errorf("GetFileSummary(%q): expected symbols; got 0", p)
			}
		}
	})

	t.Run("get_file_outline_accepts_both_path_forms", func(t *testing.T) {
		for _, p := range []string{"server.go", "services/api/server.go"} {
			items, err := reader.GetFileOutline(ctx, p, "")
			if err != nil {
				t.Fatalf("GetFileOutline(%q): %v", p, err)
			}
			if len(items) == 0 {
				t.Errorf("GetFileOutline(%q): expected outline items; got 0", p)
			}
		}
	})

	t.Run("files_tagged_with_project_id", func(t *testing.T) {
		rows, err := ix.DB().QueryContext(ctx,
			`SELECT f.path, p.name FROM files f JOIN projects p ON p.id = f.project_id`)
		if err != nil {
			t.Fatalf("join query: %v", err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var path, proj string
			if err := rows.Scan(&path, &proj); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got[path] = proj
		}
		if rows.Err() != nil {
			t.Fatalf("rows err: %v", rows.Err())
		}
		if len(got) != 3 {
			t.Errorf("expected 3 tagged files; got %d (%v)", len(got), got)
		}
	})

	_ = projectIDs // used implicitly via workspaces
}
