package query

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jdwiederstein/mycelium/internal/embed"
)

// scoredID pairs a chunk id with its cosine score, used to shuttle top-k
// results between the scan pass and the hydrate pass.
type scoredID struct {
	ID    int64
	Score float32
}

// Searcher is an embedder-backed semantic search handle. It is created
// per-query so the caller can pick up the current configured embedder
// (the daemon may have swapped models). The Reader keeps no reference to
// an Embedder to preserve the "query doesn't own state" boundary.
//
// VSSTable, when non-empty, names a sqlite-vec vec0 virtual table whose
// rowids mirror chunks.id. When set, SearchSemantic issues a KNN query
// against it and gets O(log n) lookup (with HNSW, once sqlite-vec ships
// it; current vec0 is exact-flat but SIMD-accelerated, still much faster
// than Go). Empty VSSTable falls back to the brute-force Go path.
type Searcher struct {
	Reader   *Reader
	Embedder embed.Embedder
	VSSTable string
}

// SemanticHit is a single result from a semantic search.
type SemanticHit struct {
	ChunkID    int64   `json:"chunk_id"`
	Score      float32 `json:"score"` // cosine similarity in [-1, 1]; higher is closer
	Path       string  `json:"path"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	SymbolID   int64   `json:"symbol_id,omitempty"`
	Qualified  string  `json:"qualified,omitempty"`
	Kind       string  `json:"kind,omitempty"`
	Signature  string  `json:"signature,omitempty"`
	Snippet    string  `json:"snippet,omitempty"`
}

// SearchSemantic embeds the query text, then scans chunks.embedding doing
// cosine similarity in Go. Brute-force O(n * d) is fine for repos with a
// few tens of thousands of chunks; HNSW via sqlite-vec slots in later.
//
// project (v1.5) scopes the candidate chunks to a single workspace.
// pathsIn (v1.6) is the --since path filter. When either is set, the
// vec0 fast path is skipped (vec0 KNN doesn't compose with arbitrary
// WHERE clauses); brute-force two-pass handles both correctly.
func (s *Searcher) SearchSemantic(ctx context.Context, query string, k int, kind, pathContains, project string, pathsIn []string) ([]SemanticHit, error) {
	if s.Embedder == nil {
		return nil, embed.ErrNotConfigured
	}
	if _, ok := s.Embedder.(embed.Noop); ok {
		return nil, embed.ErrNotConfigured
	}
	if k <= 0 {
		k = 10
	}

	qvecs, err := s.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(qvecs) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors", len(qvecs))
	}
	qv := qvecs[0]
	dim := s.Embedder.Dimension()
	qPacked := embed.Pack(qv)

	// vec0 fast path. We only use it when every filter is empty so we
	// can let the KNN query do the whole job. With filters (kind/path/
	// project/pathsIn) we fall back to brute-force so we don't accidentally
	// return <k results after post-hoc filtering.
	if s.VSSTable != "" && kind == "" && pathContains == "" && project == "" && pathsIn == nil {
		if hits, ok, err := s.searchViaVSS(ctx, qPacked, k); err != nil {
			return nil, err
		} else if ok {
			return hits, nil
		}
		// ok=false means the table doesn't exist for this dim or the
		// query failed softly — drop to brute-force instead of erroring.
	}

	scope, scopeArgs, err := s.Reader.projectScope(ctx, project)
	if err != nil {
		return nil, err
	}
	pathClause, pathArgs, err := pathsInClause(pathsIn)
	if err != nil {
		return nil, err
	}

	// Pass 1: scan only the vector columns to find the top-k by cosine.
	// Pulling symbol/file metadata for every chunk (including content
	// strings) dominated the query time — at 10k×768 that's ~30 MB of
	// embedding plus untold bytes of content + path + signature per row.
	// Restricting the scan to (id, embedding, kind/path when filtering)
	// cuts the hot-loop allocation drastically.
	// We always JOIN files now because project scope requires it. The
	// kind/path filters are ANDed in conditionally. Avoids the
	// combinatoric switch from v1.4 at a tiny query-plan cost.
	scanSQL := `
		SELECT c.id, c.embedding
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		LEFT JOIN symbols sym ON sym.id = c.symbol_id
		WHERE c.embedding IS NOT NULL AND c.embed_model = ?`
	scanArgs := []any{s.Embedder.Model()}
	if kind != "" {
		scanSQL += ` AND sym.kind = ?`
		scanArgs = append(scanArgs, kind)
	}
	if pathContains != "" {
		scanSQL += ` AND f.path LIKE ?`
		scanArgs = append(scanArgs, "%"+pathContains+"%")
	}
	if scope != "" {
		scanSQL += scope
		scanArgs = append(scanArgs, scopeArgs...)
	}
	if pathClause != "" {
		scanSQL += pathClause
		scanArgs = append(scanArgs, pathArgs...)
	}
	rows, err := s.Reader.db.QueryContext(ctx, scanSQL, scanArgs...)
	if err != nil {
		return nil, err
	}

	var top []scoredID
	insert := func(sl []scoredID, x scoredID) []scoredID {
		if len(sl) < k {
			sl = append(sl, x)
			sort.Slice(sl, func(i, j int) bool { return sl[i].Score > sl[j].Score })
			return sl
		}
		if x.Score <= sl[len(sl)-1].Score {
			return sl
		}
		sl[len(sl)-1] = x
		sort.Slice(sl, func(i, j int) bool { return sl[i].Score > sl[j].Score })
		return sl
	}
	// Reusable buffer to decode each embedding into — avoids 10k fresh
	// allocations per scan.
	buf := make([]float32, dim)
	for rows.Next() {
		var chunkID int64
		var embBytes []byte
		if err := rows.Scan(&chunkID, &embBytes); err != nil {
			rows.Close()
			return nil, err
		}
		if len(embBytes) != 4*dim {
			continue // stale row from a model switch
		}
		if err := embed.UnpackInto(embBytes, buf); err != nil {
			continue
		}
		top = insert(top, scoredID{ID: chunkID, Score: embed.Cosine(qv, buf)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(top) == 0 {
		return nil, nil
	}

	// Pass 2: hydrate metadata + snippet only for the top-k.
	return s.hydrateHits(ctx, top, k)
}

// hydrateHits turns (chunkID, score) pairs into full SemanticHit rows by
// fetching the display fields in one query. Used by both the brute-force
// and the vec0 paths.
func (s *Searcher) hydrateHits(ctx context.Context, scored []scoredID, k int) ([]SemanticHit, error) {
	if len(scored) == 0 {
		return nil, nil
	}
	ids := make([]any, len(scored))
	for i, sc := range scored {
		ids[i] = sc.ID
	}
	placeholders := "?" + strings.Repeat(",?", len(scored)-1)
	hydrateSQL := `
		SELECT c.id, c.start_line, c.end_line, c.content,
		       f.path, COALESCE(c.symbol_id, 0),
		       COALESCE(sym.qualified, ''), COALESCE(sym.kind, ''), COALESCE(sym.signature, '')
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		LEFT JOIN symbols sym ON sym.id = c.symbol_id
		WHERE c.id IN (` + placeholders + `)`
	rows, err := s.Reader.db.QueryContext(ctx, hydrateSQL, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[int64]SemanticHit, len(scored))
	for rows.Next() {
		var h SemanticHit
		var content string
		if err := rows.Scan(&h.ChunkID, &h.StartLine, &h.EndLine, &content,
			&h.Path, &h.SymbolID, &h.Qualified, &h.Kind, &h.Signature); err != nil {
			return nil, err
		}
		h.Snippet = firstLines(content, 8)
		byID[h.ChunkID] = h
	}
	// Stitch scores back in score-descending order.
	out := make([]SemanticHit, 0, len(scored))
	for _, sc := range scored {
		if h, ok := byID[sc.ID]; ok {
			h.Score = sc.Score
			out = append(out, h)
		}
	}
	return out, rows.Err()
}

// searchViaVSS runs a KNN query against the sqlite-vec virtual table named
// in s.VSSTable. Returns (hits, true, nil) on success, (nil, false, nil)
// when the table isn't usable for the current embedder (wrong dim, missing),
// (nil, false, err) on an actual error we don't want to swallow.
//
// vec0 returns `distance` (L2 by default) so we convert to cosine-ish by
// noting that for unit-norm vectors, cosine_sim = 1 - (L2^2 / 2). Most
// embedders return unit-norm; if yours doesn't, scores are still
// monotonically correct for ranking but not comparable to the brute-force
// path's cosine. This is documented in LIMITATIONS.md.
func (s *Searcher) searchViaVSS(ctx context.Context, qPacked []byte, k int) ([]SemanticHit, bool, error) {
	if s.VSSTable == "" {
		return nil, false, nil
	}
	// KNN: the vec0 virtual table requires `embedding MATCH ? AND k = ?`
	// in the WHERE and an ORDER BY distance. We select rowid (= chunks.id)
	// plus distance, then join to chunks/files/symbols for the display fields.
	knnSQL := fmt.Sprintf(`
		WITH knn AS (
		    SELECT rowid AS chunk_id, distance
		    FROM %s
		    WHERE embedding MATCH ? AND k = ?
		)
		SELECT c.id, c.start_line, c.end_line, c.content,
		       f.path, COALESCE(c.symbol_id, 0),
		       COALESCE(sym.qualified, ''), COALESCE(sym.kind, ''), COALESCE(sym.signature, ''),
		       knn.distance
		FROM knn
		JOIN chunks c ON c.id = knn.chunk_id
		JOIN files f ON f.id = c.file_id
		LEFT JOIN symbols sym ON sym.id = c.symbol_id
		ORDER BY knn.distance`, s.VSSTable)

	rows, err := s.Reader.db.QueryContext(ctx, knnSQL, qPacked, k)
	if err != nil {
		// Most likely "no such table" when dim changed or extension reloaded.
		// Signal a soft failure so the caller drops to brute-force.
		return nil, false, nil
	}
	defer rows.Close()

	var hits []SemanticHit
	for rows.Next() {
		var (
			h       SemanticHit
			content string
			dist    float64
		)
		if err := rows.Scan(&h.ChunkID, &h.StartLine, &h.EndLine, &content,
			&h.Path, &h.SymbolID, &h.Qualified, &h.Kind, &h.Signature, &dist); err != nil {
			return nil, false, err
		}
		// L2 -> approximate cosine for unit vectors. Higher-is-better.
		h.Score = float32(1.0 - (dist * dist / 2.0))
		h.Snippet = firstLines(content, 8)
		hits = append(hits, h)
	}
	return hits, true, rows.Err()
}

// insertTopK keeps the slice sorted descending by Score, bounded by k.
func insertTopK(slice []SemanticHit, hit SemanticHit, k int) []SemanticHit {
	// Fast path: below capacity, just append and sort.
	if len(slice) < k {
		slice = append(slice, hit)
		sort.Slice(slice, func(i, j int) bool { return slice[i].Score > slice[j].Score })
		return slice
	}
	// Reject hits worse than the current minimum.
	if hit.Score <= slice[len(slice)-1].Score {
		return slice
	}
	slice[len(slice)-1] = hit
	sort.Slice(slice, func(i, j int) bool { return slice[i].Score > slice[j].Score })
	return slice
}

func containsASCII(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func firstLines(s string, n int) string {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			count++
			if count == n {
				return s[:i]
			}
		}
	}
	return s
}

// EmbeddingStatus counts how many chunks have been embedded vs still pending.
// Useful for the stats endpoint and for agents deciding whether to wait.
type EmbeddingStatus struct {
	ChunksTotal   int    `json:"chunks_total"`
	ChunksEmbed   int    `json:"chunks_embedded"`
	Pending       int    `json:"pending"`
	Model         string `json:"model"`
}

func (r *Reader) EmbeddingStatus(ctx context.Context) (EmbeddingStatus, error) {
	var s EmbeddingStatus
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&s.ChunksTotal); err != nil {
		return s, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE embedding IS NOT NULL`).Scan(&s.ChunksEmbed); err != nil {
		return s, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embed_queue`).Scan(&s.Pending); err != nil {
		return s, err
	}
	var model sql.NullString
	_ = r.db.QueryRowContext(ctx, `SELECT embed_model FROM chunks WHERE embed_model IS NOT NULL LIMIT 1`).Scan(&model)
	s.Model = model.String
	return s, nil
}
