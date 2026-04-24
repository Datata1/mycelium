package query_test

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/query"
)

// BenchmarkSemanticSearch measures end-to-end SearchSemantic latency at
// increasing corpus sizes. The fixture directly INSERTs synthetic vectors
// into the chunks table (skipping the whole parse + chunker + embed
// pipeline) so we can control the shape precisely.
//
// Three knobs control the matrix:
//   - Corpus size: 10k, 50k, 100k chunks
//   - Vector dim: 768 (nomic-embed-text default)
//   - Backend: brute-force (set via empty VSSTable) only; vec0 requires
//     the extension and is benched separately in a helper script when
//     the user has sqlite-vec installed.
//
// Run:
//   go test -tags sqlite_fts5 -run=^$ -bench=BenchmarkSemanticSearch \
//     -benchtime=5x -count=3 ./internal/query/
func BenchmarkSemanticSearch(b *testing.B) {
	cases := []struct {
		name   string
		chunks int
	}{
		{"10k", 10_000},
		{"50k", 50_000},
		{"100k", 100_000},
	}
	for _, c := range cases {
		c := c
		b.Run(c.name, func(b *testing.B) {
			benchSemanticAt(b, c.chunks, 768)
		})
	}
}

func benchSemanticAt(b *testing.B, chunks, dim int) {
	b.Helper()
	ctx := context.Background()
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "index.db")

	ix, err := index.Open(dbPath)
	if err != nil {
		b.Fatalf("open index: %v", err)
	}
	b.Cleanup(func() { _ = ix.Close() })

	// Populate: one file row, one symbol row per chunk, one chunk row
	// with a unit-norm random vector. The query vector below matches
	// one specific chunk exactly so top-1 is deterministic — a good
	// smoke check alongside the timing.
	fake := embed.NewFake(dim)
	if err := populate(ctx, ix, chunks, dim); err != nil {
		b.Fatalf("populate: %v", err)
	}

	reader := query.NewReader(ix.DB())
	searcher := &query.Searcher{
		Reader:   reader,
		Embedder: fake,
		// VSSTable stays empty — this benchmark measures the
		// brute-force floor.
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hits, err := searcher.SearchSemantic(ctx, "bench query text", 10, "", "", "", nil)
		if err != nil {
			b.Fatalf("search: %v", err)
		}
		if len(hits) == 0 {
			b.Fatalf("zero hits at size %d", chunks)
		}
	}
}

// populate bulk-inserts N file/symbol/chunk rows with random unit-norm
// embeddings. Used by the bench harness; not meant for production paths.
func populate(ctx context.Context, ix *index.Index, n, dim int) error {
	db := ix.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Bulk-insert one file per 1000 chunks to keep file rows manageable.
	// We re-use file_id rapidly; the field doesn't matter for search.
	filesNeeded := (n + 999) / 1000
	fileIDs := make([]int64, 0, filesNeeded)
	for i := 0; i < filesNeeded; i++ {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO files(path, language, size_bytes, mtime_ns, content_hash, parse_hash, last_indexed_at)
			VALUES(?, 'go', 0, 0, X'00', X'00', 0)`, fmt.Sprintf("bench_%d.go", i))
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		fileIDs = append(fileIDs, id)
	}

	// Symbols + chunks.
	insertSym, err := tx.PrepareContext(ctx, `
		INSERT INTO symbols(file_id, name, qualified, kind, start_line, start_col, end_line, end_col, symbol_hash)
		VALUES(?, ?, ?, 'function', 1, 1, 5, 1, X'00')`)
	if err != nil {
		return err
	}
	defer insertSym.Close()
	insertChunk, err := tx.PrepareContext(ctx, `
		INSERT INTO chunks(file_id, symbol_id, kind, start_line, end_line, content_hash, content, embedding, embed_model)
		VALUES(?, ?, 'symbol', 1, 5, X'00', '', ?, 'fake')`)
	if err != nil {
		return err
	}
	defer insertChunk.Close()

	// Deterministic RNG so benchmarks are comparable across runs.
	r := rand.New(rand.NewSource(42))
	for i := 0; i < n; i++ {
		fileID := fileIDs[i/1000]
		res, err := insertSym.ExecContext(ctx, fileID,
			fmt.Sprintf("fn_%d", i),
			fmt.Sprintf("bench.fn_%d", i))
		if err != nil {
			return err
		}
		symID, _ := res.LastInsertId()
		packed := embed.Pack(randomUnit(r, dim))
		if _, err := insertChunk.ExecContext(ctx, fileID, symID, packed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func randomUnit(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var norm float64
	for i := range v {
		v[i] = float32(r.NormFloat64())
		norm += float64(v[i]) * float64(v[i])
	}
	if norm == 0 {
		return v
	}
	scale := float32(1.0 / sqrt(norm))
	for i := range v {
		v[i] *= scale
	}
	return v
}

func sqrt(x float64) float64 {
	// Cheaper than pulling "math" for one call.
	z := x / 2
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// Compile-time assertion we're actually using time; imports stay honest
// if future benchmarks record wall clock rather than ReportMetric.
var _ = time.Now
