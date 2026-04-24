package pipeline_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
)

// BenchmarkIndexSynthetic measures initial-index throughput on a generated
// Go-only fixture at several sizes. Run one size at a time with:
//
//	go test -tags sqlite_fts5 -run=^$ -bench=BenchmarkIndexSynthetic/10k -benchtime=1x -count=3 ./internal/pipeline/
//
// Results feed the doctor doc + README benchmark table.
func BenchmarkIndexSynthetic(b *testing.B) {
	cases := []struct {
		name    string
		symbols int
	}{
		{"1k", 1_000},
		{"10k", 10_000},
		{"50k", 50_000},
	}
	for _, c := range cases {
		c := c
		b.Run(c.name, func(b *testing.B) {
			benchIndexAt(b, c.symbols)
		})
	}
}

func benchIndexAt(b *testing.B, symbols int) {
	b.Helper()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		dir := b.TempDir()
		files, err := pipeline.GenerateSyntheticRepo(dir, symbols)
		if err != nil {
			b.Fatalf("generate: %v", err)
		}
		ix, err := index.Open(filepath.Join(dir, ".mycelium", "index.db"))
		if err != nil {
			b.Fatalf("open index: %v", err)
		}
		reg := parser.NewRegistry()
		reg.Register(golang.New())
		walker := repo.NewWalker(dir, []string{"**/*.go"}, nil, 0)
		p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: walker, Embedder: embed.Noop{}}
		b.StartTimer()

		rep, err := p.RunOnce(context.Background())
		if err != nil {
			b.Fatalf("RunOnce: %v", err)
		}
		b.StopTimer()

		if rep.FilesChanged != files {
			b.Fatalf("expected %d files changed, got %d", files, rep.FilesChanged)
		}
		if err := ix.Close(); err != nil {
			b.Fatalf("close: %v", err)
		}

		b.ReportMetric(float64(rep.Symbols)/b.Elapsed().Seconds(), "sym/sec")
	}
}

// BenchmarkQueryFindSymbol measures point lookup latency after indexing.
// Keeps the same generated fixture alive across the timed loop so only
// query work is measured.
func BenchmarkQueryFindSymbol(b *testing.B) {
	dir := b.TempDir()
	if _, err := pipeline.GenerateSyntheticRepo(dir, 10_000); err != nil {
		b.Fatalf("generate: %v", err)
	}
	ix, err := index.Open(filepath.Join(dir, ".mycelium", "index.db"))
	if err != nil {
		b.Fatalf("open index: %v", err)
	}
	defer ix.Close()
	reg := parser.NewRegistry()
	reg.Register(golang.New())
	walker := repo.NewWalker(dir, []string{"**/*.go"}, nil, 0)
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: walker, Embedder: embed.Noop{}}
	if _, err := p.RunOnce(context.Background()); err != nil {
		b.Fatalf("RunOnce: %v", err)
	}
	r := query.NewReader(ix.DB())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.FindSymbol(context.Background(), "Action0500_4", "", "", 10, nil); err != nil {
			b.Fatalf("FindSymbol: %v", err)
		}
	}
}

// BenchmarkQueryNeighborhood measures depth-2 recursive CTE cost.
func BenchmarkQueryNeighborhood(b *testing.B) {
	dir := b.TempDir()
	if _, err := pipeline.GenerateSyntheticRepo(dir, 10_000); err != nil {
		b.Fatalf("generate: %v", err)
	}
	ix, err := index.Open(filepath.Join(dir, ".mycelium", "index.db"))
	if err != nil {
		b.Fatalf("open index: %v", err)
	}
	defer ix.Close()
	reg := parser.NewRegistry()
	reg.Register(golang.New())
	walker := repo.NewWalker(dir, []string{"**/*.go"}, nil, 0)
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: walker, Embedder: embed.Noop{}}
	if _, err := p.RunOnce(context.Background()); err != nil {
		b.Fatalf("RunOnce: %v", err)
	}
	r := query.NewReader(ix.DB())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.GetNeighborhood(context.Background(), "pkg0500.NewKind500", "", 2, query.DirBoth); err != nil {
			b.Fatalf("neighborhood: %v", err)
		}
	}
}
