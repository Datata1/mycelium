package query

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/jdwiederstein/mycelium/internal/embed"
)

// Searcher is an embedder-backed semantic search handle. It is created
// per-query so the caller can pick up the current configured embedder
// (the daemon may have swapped models). The Reader keeps no reference to
// an Embedder to preserve the "query doesn't own state" boundary.
type Searcher struct {
	Reader   *Reader
	Embedder embed.Embedder
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
func (s *Searcher) SearchSemantic(ctx context.Context, query string, k int, kind, pathContains string) ([]SemanticHit, error) {
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

	rows, err := s.Reader.db.QueryContext(ctx, `
		SELECT c.id, c.embedding, c.start_line, c.end_line, c.content,
		       f.path, COALESCE(c.symbol_id, 0),
		       COALESCE(sym.qualified, ''), COALESCE(sym.kind, ''), COALESCE(sym.signature, '')
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		LEFT JOIN symbols sym ON sym.id = c.symbol_id
		WHERE c.embedding IS NOT NULL AND c.embed_model = ?`, s.Embedder.Model())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Top-k with a simple min-heap-like structure: keep a sorted slice
	// of size k and insert in order. For realistic k (<=50) this is
	// cheaper than pulling in a heap abstraction.
	var top []SemanticHit
	for rows.Next() {
		var (
			chunkID    int64
			embBytes   []byte
			startLine  int
			endLine    int
			content    string
			path       string
			symbolID   int64
			qualified  string
			kindRow    string
			signature  string
		)
		if err := rows.Scan(&chunkID, &embBytes, &startLine, &endLine, &content, &path, &symbolID, &qualified, &kindRow, &signature); err != nil {
			return nil, err
		}
		if kind != "" && kindRow != kind {
			continue
		}
		if pathContains != "" && !containsASCII(path, pathContains) {
			continue
		}
		v, err := embed.Unpack(embBytes, dim)
		if err != nil {
			// Stale row — likely a model switch happened mid-query. Skip.
			continue
		}
		score := embed.Cosine(qv, v)
		hit := SemanticHit{
			ChunkID:   chunkID,
			Score:     score,
			Path:      path,
			StartLine: startLine,
			EndLine:   endLine,
			SymbolID:  symbolID,
			Qualified: qualified,
			Kind:      kindRow,
			Signature: signature,
			Snippet:   firstLines(content, 8),
		}
		top = insertTopK(top, hit, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return top, nil
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
