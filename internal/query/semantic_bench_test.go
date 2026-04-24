package query_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/query"
)

// BenchmarkSemanticSearch measures end-to-end SearchSemantic latency at
// increasing corpus sizes and vector dimensions. The fixture directly
// INSERTs synthetic vectors into the chunks table (skipping the whole
// parse + chunker + embed pipeline) so we can control the shape precisely.
//
// Matrix:
//   - Corpus size: 10k, 50k, 100k chunks
//   - Vector dim: 384, 768, 1536
//   - Backend: brute-force Go cosine (always) + vec0 (when
//     $MYCELIUM_VEC_PATH points at a loadable sqlite-vec shared
//     library; skipped otherwise so CI without the extension stays green)
//
// Run:
//   MYCELIUM_VEC_PATH=/path/to/vec0.so \
//     go test -tags sqlite_fts5 -run=^$ -bench=BenchmarkSemanticSearch \
//     -benchtime=5x -count=3 ./internal/query/
func BenchmarkSemanticSearch(b *testing.B) {
	sizes := []struct {
		name   string
		chunks int
	}{
		{"10k", 10_000},
		{"50k", 50_000},
		{"100k", 100_000},
	}
	dims := []int{384, 768, 1536}
	extPath := os.Getenv("MYCELIUM_VEC_PATH")

	for _, sz := range sizes {
		for _, dim := range dims {
			name := fmt.Sprintf("%s_%ddim/brute", sz.name, dim)
			b.Run(name, func(b *testing.B) {
				benchSemanticAt(b, sz.chunks, dim, "")
			})
			if extPath != "" {
				name := fmt.Sprintf("%s_%ddim/vec0", sz.name, dim)
				b.Run(name, func(b *testing.B) {
					benchSemanticAt(b, sz.chunks, dim, extPath)
				})
			}
		}
	}
}

// benchSemanticAt populates a fresh index with `chunks` synthetic vectors
// at the given dimension, then times SearchSemantic. When extPath is
// non-empty the sqlite-vec extension is loaded and the vec0 fast path
// is exercised; otherwise Searcher falls back to brute-force.
func benchSemanticAt(b *testing.B, chunks, dim int, extPath string) {
	b.Helper()
	ctx := context.Background()
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "index.db")

	ix, err := index.OpenWithExtension(dbPath, extPath)
	if err != nil {
		b.Fatalf("open index: %v", err)
	}
	b.Cleanup(func() { _ = ix.Close() })

	if extPath != "" {
		if err := ix.EnsureVSS(ctx, dim); err != nil {
			b.Fatalf("ensure vss: %v", err)
		}
		if !ix.VSSAvailable() {
			b.Fatalf("vec0 unavailable after EnsureVSS — extension path wrong?")
		}
	}

	fake := embed.NewFake(dim)
	if err := populate(ctx, ix, chunks, dim, ix.VSSTableName()); err != nil {
		b.Fatalf("populate: %v", err)
	}

	reader := query.NewReader(ix.DB())
	searcher := &query.Searcher{
		Reader:   reader,
		Embedder: fake,
		VSSTable: ix.VSSTableName(), // "" falls back to brute-force
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
// embeddings. When vssTable is non-empty the same vectors are mirrored
// into the vec0 virtual table so the KNN path actually sees them.
func populate(ctx context.Context, ix *index.Index, n, dim int, vssTable string) error {
	db := ix.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

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

	var insertVSS *sql.Stmt
	if vssTable != "" {
		insertVSS, err = tx.PrepareContext(ctx,
			fmt.Sprintf(`INSERT INTO %s(rowid, embedding) VALUES(?, ?)`, vssTable))
		if err != nil {
			return err
		}
		defer insertVSS.Close()
	}

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
		chunkRes, err := insertChunk.ExecContext(ctx, fileID, symID, packed)
		if err != nil {
			return err
		}
		if insertVSS != nil {
			chunkID, _ := chunkRes.LastInsertId()
			if _, err := insertVSS.ExecContext(ctx, chunkID, packed); err != nil {
				return err
			}
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
	z := x / 2
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}

var _ = time.Now
