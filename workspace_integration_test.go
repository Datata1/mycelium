// Integration test for v1.5 workspace mode. Exercises a 3-project
// fixture (Go api, TS web, Python worker) end-to-end: indexing tags
// files with their project_id, and queries correctly scope by project
// name.
package mycelium_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/parser/python"
	"github.com/jdwiederstein/mycelium/internal/parser/typescript"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
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
		Embedder:   embed.Noop{},
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
			hits, err := reader.FindSymbol(ctx, "APIOnlySymbol", "", tc.project, 10, nil)
			if err != nil {
				t.Fatalf("find_symbol project=%q: %v", tc.project, err)
			}
			if len(hits) != tc.want {
				t.Errorf("find_symbol APIOnlySymbol project=%q: got %d hits, want %d",
					tc.project, len(hits), tc.want)
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
			files, err := reader.ListFiles(ctx, "", "", tc.project, 100, nil)
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
		hits, err := reader.FindSymbol(ctx, "APIOnlySymbol", "", "does-not-exist", 10, nil)
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if len(hits) != 0 {
			t.Errorf("unknown project should yield 0 hits, got %d", len(hits))
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
}
