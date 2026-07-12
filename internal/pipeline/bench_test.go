package pipeline_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
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
		p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: walker}
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
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: walker}
	if _, err := p.RunOnce(context.Background()); err != nil {
		b.Fatalf("RunOnce: %v", err)
	}
	r := query.NewReader(ix.DB())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.FindSymbol(context.Background(), "Action0500_4", "", "", 10, nil, ""); err != nil {
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
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: walker}
	if _, err := p.RunOnce(context.Background()); err != nil {
		b.Fatalf("RunOnce: %v", err)
	}
	r := query.NewReader(ix.DB())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.GetNeighborhood(context.Background(), "pkg0500.NewKind500", "", 2, query.DirBoth, ""); err != nil {
			b.Fatalf("neighborhood: %v", err)
		}
	}
}

// setupChainFixture indexes a generated repo at the given size/fanout and
// returns a Reader over it. Fails the benchmark if the fixture's chain
// refs didn't resolve — timing a walk over unresolved edges would measure
// nothing.
func setupChainFixture(b *testing.B, symbols, fanout int) *query.Reader {
	b.Helper()
	dir := b.TempDir()
	if _, err := pipeline.GenerateSyntheticRepoOpts(dir, symbols, fanout); err != nil {
		b.Fatalf("generate: %v", err)
	}
	ix, err := index.Open(filepath.Join(dir, ".mycelium", "index.db"))
	if err != nil {
		b.Fatalf("open index: %v", err)
	}
	b.Cleanup(func() { ix.Close() })
	reg := parser.NewRegistry()
	reg.Register(golang.New())
	walker := repo.NewWalker(dir, []string{"**/*.go"}, nil, 0)
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: walker}
	if _, err := p.RunOnce(context.Background()); err != nil {
		b.Fatalf("RunOnce: %v", err)
	}
	r := query.NewReader(ix.DB())

	st, err := r.Stats(context.Background())
	if err != nil {
		b.Fatalf("stats: %v", err)
	}
	if st.NonImportRefs == 0 || float64(st.RefsTrulyUnresolved)/float64(st.NonImportRefs) > 0.01 {
		b.Fatalf("fixture refs unresolved: %d of %d non-import refs", st.RefsTrulyUnresolved, st.NonImportRefs)
	}
	imp, err := r.ImpactAnalysis(context.Background(), "pkg0000.Chain0", "", "", 2, nil)
	if err != nil {
		b.Fatalf("impact sanity: %v", err)
	}
	if len(imp.Hits) == 0 {
		b.Fatalf("chain edges not traversable: impact of pkg0000.Chain0 returned 0 hits")
	}
	return r
}

// BenchmarkQueryImpactAnalysis measures the inbound recursive CTE at and
// beyond the default depth on sparse (fanout 2) and dense (fanout 8)
// chain graphs. The rows metric is the result-set size — on dense graphs
// the cardinality at depth 10 matters as much as the latency.
func BenchmarkQueryImpactAnalysis(b *testing.B) {
	for _, size := range []int{10_000, 50_000} {
		for _, fanout := range []int{2, 8} {
			b.Run(fmt.Sprintf("%dk-fan%d", size/1000, fanout), func(b *testing.B) {
				r := setupChainFixture(b, size, fanout)
				for _, depth := range []int{5, 10} {
					b.Run(fmt.Sprintf("depth%d", depth), func(b *testing.B) {
						var hits int
						for i := 0; i < b.N; i++ {
							res, err := r.ImpactAnalysis(context.Background(), "pkg0000.Chain0", "", "", depth, nil)
							if err != nil {
								b.Fatalf("impact: %v", err)
							}
							hits = len(res.Hits)
						}
						b.ReportMetric(float64(hits), "rows")
					})
				}
			})
		}
	}
}

// BenchmarkQueryCriticalPath measures the layered shortest-path BFS at
// the depth cap. The dense case is the one that blew up (~24s) under
// the previous all-acyclic-paths CTE.
func BenchmarkQueryCriticalPath(b *testing.B) {
	for _, size := range []int{10_000, 50_000} {
		for _, fanout := range []int{2, 8} {
			b.Run(fmt.Sprintf("%dk-fan%d", size/1000, fanout), func(b *testing.B) {
				r := setupChainFixture(b, size, fanout)
				b.ResetTimer()
				var paths int
				for i := 0; i < b.N; i++ {
					// Chain{i} always calls Chain{i+1}, so pkg0006.Chain6 is
					// reachable in ≤6 hops from pkg0000.Chain0 at any fanout.
					res, err := r.CriticalPath(context.Background(), "pkg0000.Chain0", "pkg0006.Chain6", "", 8, 5)
					if err != nil {
						b.Fatalf("critical path: %v", err)
					}
					if len(res.Paths) == 0 {
						b.Fatal("critical path: no path found")
					}
					paths = len(res.Paths)
				}
				b.ReportMetric(float64(paths), "paths")
			})
		}
	}
}

// BenchmarkQueryNeighborhoodDeep complements BenchmarkQueryNeighborhood
// (depth 2) with the depth cap on sparse and dense chain graphs.
func BenchmarkQueryNeighborhoodDeep(b *testing.B) {
	for _, size := range []int{10_000, 50_000} {
		for _, fanout := range []int{2, 8} {
			b.Run(fmt.Sprintf("%dk-fan%d", size/1000, fanout), func(b *testing.B) {
				r := setupChainFixture(b, size, fanout)
				b.ResetTimer()
				var nodes int
				for i := 0; i < b.N; i++ {
					res, err := r.GetNeighborhood(context.Background(), "pkg0000.Chain0", "", 5, query.DirBoth, "")
					if err != nil {
						b.Fatalf("neighborhood: %v", err)
					}
					nodes = len(res.Nodes)
				}
				b.ReportMetric(float64(nodes), "nodes")
			})
		}
	}
}

// BenchmarkQueryInboundClosureFiles measures the multi-seed CTE behind
// select_tests: many seeds at once (a whole changed-file's symbol set)
// against the same chain fixtures as ImpactAnalysis.
func BenchmarkQueryInboundClosureFiles(b *testing.B) {
	for _, size := range []int{10_000, 50_000} {
		for _, fanout := range []int{2, 8} {
			b.Run(fmt.Sprintf("%dk-fan%d", size/1000, fanout), func(b *testing.B) {
				r := setupChainFixture(b, size, fanout)
				// Seed with a mid-chain band of symbols, the shape a
				// multi-file diff produces.
				syms, err := r.SymbolsInFiles(context.Background(), chainFilePaths(40, 60))
				if err != nil {
					b.Fatalf("seeds: %v", err)
				}
				seeds := make([]int64, len(syms))
				for i, s := range syms {
					seeds[i] = s.ID
				}
				if len(seeds) == 0 {
					b.Fatal("no seeds")
				}
				b.ResetTimer()
				var files int
				for i := 0; i < b.N; i++ {
					hits, err := r.InboundClosureFiles(context.Background(), seeds, 5)
					if err != nil {
						b.Fatalf("closure: %v", err)
					}
					files = len(hits)
				}
				b.ReportMetric(float64(files), "files")
			})
		}
	}
}

// chainFilePaths returns the fixture file names pkg%04d.go for [from,to).
func chainFilePaths(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("pkg%04d.go", i))
	}
	return out
}
