// Integration tests for the reconcile behaviour of RunOnce: rows for
// files deleted from disk are pruned (with cascades), the prune respects
// workspace boundaries, and a completed reconcile records
// last_full_scan_at in index_meta.
package integration_test

import (
	"context"
	"os"
	"path/filepath"
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

func TestIntegration_ReconcilePrunesDeletedFiles(t *testing.T) {
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

	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   repo.NewWalker(dst, []string{"**/*.go", "src/**/*.ts", "py/**/*.py"}, nil, 0),
	}
	before := time.Now().Add(-time.Second)
	rep, err := p.RunOnce(ctx)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if rep.FilesPruned != 0 {
		t.Fatalf("first run pruned %d files; want 0", rep.FilesPruned)
	}

	reader := query.NewReader(ix.DB())

	t.Run("last_full_scan_at_recorded", func(t *testing.T) {
		ts, ok, err := reader.LastFullScanAt(ctx)
		if err != nil {
			t.Fatalf("LastFullScanAt: %v", err)
		}
		if !ok {
			t.Fatal("expected last_full_scan_at after a completed reconcile")
		}
		if ts.Before(before) {
			t.Errorf("last_full_scan_at = %v, want >= %v", ts, before)
		}
	})

	// Simulate a delete the watcher never saw (daemon down, branch
	// switch): remove the file on disk, then reconcile.
	if err := os.Remove(filepath.Join(dst, "py", "worker.py")); err != nil {
		t.Fatalf("remove fixture file: %v", err)
	}
	rep, err = p.RunOnce(ctx)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if rep.FilesPruned != 1 {
		t.Fatalf("pruned %d files; want 1", rep.FilesPruned)
	}

	t.Run("row_and_symbols_gone", func(t *testing.T) {
		s, err := reader.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if s.Files != 3 {
			t.Errorf("files after prune: got %d, want 3", s.Files)
		}
		res, err := reader.FindSymbol(ctx, "JobQueue", "", "", 10, nil, "")
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if len(res.Matches) != 0 {
			t.Errorf("symbols of pruned file still findable: %v", names(res.Matches))
		}
	})

	t.Run("stable_second_reconcile", func(t *testing.T) {
		rep, err := p.RunOnce(ctx)
		if err != nil {
			t.Fatalf("reindex: %v", err)
		}
		if rep.FilesPruned != 0 {
			t.Errorf("stable reconcile pruned %d files; want 0", rep.FilesPruned)
		}
	})
}

func TestIntegration_ReconcilePruneRespectsWorkspaces(t *testing.T) {
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
	var workspaces []pipeline.Workspace
	for _, pr := range projects {
		id, err := ix.UpsertProject(ctx, pr.name, pr.root)
		if err != nil {
			t.Fatalf("upsert %s: %v", pr.name, err)
		}
		w := repo.NewWalker(filepath.Join(dst, pr.root), pr.include, nil, 0)
		workspaces = append(workspaces, pipeline.Workspace{ProjectID: id, Walker: w})
	}
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Workspaces: workspaces}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}

	if err := os.Remove(filepath.Join(dst, "services/web/src/app.ts")); err != nil {
		t.Fatalf("remove fixture file: %v", err)
	}
	rep, err := p.RunOnce(ctx)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if rep.FilesPruned != 1 {
		t.Fatalf("pruned %d files; want 1", rep.FilesPruned)
	}

	reader := query.NewReader(ix.DB())
	for _, tc := range []struct {
		project string
		want    int
	}{
		{"api", 1},
		{"web", 0},
		{"worker", 1},
	} {
		files, err := reader.ListFiles(ctx, "", "", tc.project, 100, nil)
		if err != nil {
			t.Fatalf("list_files project=%q: %v", tc.project, err)
		}
		if len(files) != tc.want {
			t.Errorf("project %q: got %d files, want %d", tc.project, len(files), tc.want)
		}
	}
}
