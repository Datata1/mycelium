// Integration tests for TS/Python inheritance edges (field-test finding:
// "who extends Pipeline?" forced a grep because only Go emitted
// RefInherit). The TS emitter also depends on abstract classes being
// indexed at all — abstract_class_declaration is a distinct tree-sitter
// node type the parser previously skipped.
package integration_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/python"
	"github.com/datata1/mycelium/internal/parser/typescript"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
	pyresolver "github.com/datata1/mycelium/internal/resolver/python"
	tsresolver "github.com/datata1/mycelium/internal/resolver/typescript"
)

func TestIntegration_TypeScriptInheritance(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()

	writeFile(t, dst, "src/pipeline.ts", `export abstract class Pipeline {
    public abstract start(): void;
}

export interface Restartable {
    restart(): void;
}
`)
	writeFile(t, dst, "src/replicas.ts", `import { Pipeline, Restartable } from './pipeline.js';

export class ReplicasPipeline extends Pipeline implements Restartable {
    start(): void {}
    restart(): void {}
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	t.Cleanup(func() { ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(typescript.New())

	walker := repo.NewWalker(dst, []string{"src/**/*.ts"}, nil, 0)
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   walker,
		Resolvers: map[string]pipeline.Resolver{
			"typescript": tsresolver.New(),
		},
	}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}

	r := query.NewReader(ix.DB())

	// The abstract base class must be indexed at all (parser previously
	// skipped abstract_class_declaration entirely).
	t.Run("abstract_class_is_indexed", func(t *testing.T) {
		res, err := r.FindSymbol(ctx, "Pipeline", "class", "", 10, nil, "")
		if err != nil {
			t.Fatalf("FindSymbol: %v", err)
		}
		if len(res.Matches) == 0 {
			t.Fatal("expected the abstract class Pipeline to be indexed; got 0 matches")
		}
	})

	// "who extends Pipeline?" — the exact field-test question.
	t.Run("extends_edge_visible_via_get_references", func(t *testing.T) {
		res, err := r.GetReferences(ctx, "Pipeline", "", 20, nil)
		if err != nil {
			t.Fatalf("GetReferences: %v", err)
		}
		var found bool
		for _, h := range res.Matches {
			if h.Kind == "inherit" && h.SrcSymbolName == "replicas.ReplicasPipeline" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected an inherit ref replicas.ReplicasPipeline -> Pipeline; got %+v", res.Matches)
		}
	})

	t.Run("implements_edge_visible_via_get_references", func(t *testing.T) {
		res, err := r.GetReferences(ctx, "Restartable", "", 20, nil)
		if err != nil {
			t.Fatalf("GetReferences: %v", err)
		}
		var found bool
		for _, h := range res.Matches {
			if h.Kind == "inherit" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected an inherit ref for implements Restartable; got %+v", res.Matches)
		}
	})

	// get_neighborhood on the base must surface the subclass via the
	// inheritance expansion (previously Go-only).
	t.Run("neighborhood_expands_to_subclass", func(t *testing.T) {
		nb, err := r.GetNeighborhood(ctx, "pipeline.Pipeline", "", 1, query.DirBoth, "")
		if err != nil {
			t.Fatalf("GetNeighborhood: %v", err)
		}
		if !hasNode(nb.Nodes, "replicas.ReplicasPipeline") {
			t.Errorf("expected replicas.ReplicasPipeline among neighbors; got %v", qualifieds(nb.Nodes))
		}
	})
}

func TestIntegration_PythonInheritance(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()

	writeFile(t, dst, "base.py", `class Task:
    def run(self):
        pass
`)
	writeFile(t, dst, "worker.py", `from base import Task

class CleanupWorker(Task):
    def run(self):
        pass
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	t.Cleanup(func() { ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(python.New())

	walker := repo.NewWalker(dst, []string{"**/*.py"}, nil, 0)
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   walker,
		Resolvers: map[string]pipeline.Resolver{
			"python": pyresolver.New(),
		},
	}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}

	r := query.NewReader(ix.DB())

	res, err := r.GetReferences(ctx, "Task", "", 20, nil)
	if err != nil {
		t.Fatalf("GetReferences: %v", err)
	}
	var found bool
	for _, h := range res.Matches {
		if h.Kind == "inherit" && h.SrcSymbolName == "worker.CleanupWorker" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected inherit ref worker.CleanupWorker -> Task; got %+v", res.Matches)
	}
}
