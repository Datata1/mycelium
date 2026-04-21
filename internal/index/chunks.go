package index

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jdwiederstein/mycelium/internal/chunker"
)

// ReplaceFileChunks wipes chunks for a file and inserts new ones, reusing
// embeddings from embed_cache where a matching content_hash already exists.
// Returns the set of content_hashes that still need embedding (cache miss)
// so the caller can enqueue them for the background worker.
func (ix *Index) ReplaceFileChunks(
	ctx context.Context,
	tx *sql.Tx,
	fileID int64,
	symbolIDs map[string]int64,
	chunks []chunker.Chunk,
	embedderModel string,
) ([]chunker.Chunk, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE file_id = ?`, fileID); err != nil {
		return nil, fmt.Errorf("delete old chunks: %w", err)
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	insert, err := tx.PrepareContext(ctx, `
		INSERT INTO chunks(file_id, symbol_id, kind, start_line, end_line, content_hash, content, embedding, embed_model)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare insert chunk: %w", err)
	}
	defer insert.Close()

	enqueue, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO embed_queue(chunk_id, enqueued_at, attempts)
		VALUES(?, ?, 0)`)
	if err != nil {
		return nil, fmt.Errorf("prepare enqueue: %w", err)
	}
	defer enqueue.Close()

	var need []chunker.Chunk
	now := time.Now().Unix()

	for _, c := range chunks {
		var symID interface{}
		if id, ok := symbolIDs[c.SymbolQualified]; ok {
			symID = id
		}
		// Look up cached embedding. Only reuse if the cached model matches
		// the active embedder — mixing dimensions would poison search.
		var emb []byte
		if embedderModel != "" && embedderModel != "none" {
			row := tx.QueryRowContext(ctx,
				`SELECT embedding FROM embed_cache WHERE content_hash = ? AND model = ?`,
				c.ContentHash, embedderModel)
			_ = row.Scan(&emb)
		}

		res, err := insert.ExecContext(ctx, fileID, symID, c.Kind, c.StartLine, c.EndLine, c.ContentHash, c.Content, emb, nullString(embedderModel))
		if err != nil {
			return nil, fmt.Errorf("insert chunk: %w", err)
		}
		chunkID, _ := res.LastInsertId()

		if emb == nil && embedderModel != "" && embedderModel != "none" {
			if _, err := enqueue.ExecContext(ctx, chunkID, now); err != nil {
				return nil, fmt.Errorf("enqueue chunk %d: %w", chunkID, err)
			}
			need = append(need, c)
		}
	}
	return need, nil
}

// WriteEmbedding stores a computed vector for a chunk and its content-hash
// cache entry. Both updates happen in one transaction so a mid-write crash
// leaves a consistent state.
func (ix *Index) WriteEmbedding(ctx context.Context, chunkID int64, contentHash, embedding []byte, model string) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE chunks SET embedding = ?, embed_model = ? WHERE id = ?`,
		embedding, model, chunkID); err != nil {
		return fmt.Errorf("update chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO embed_cache(content_hash, model, embedding, created_at)
		 VALUES(?, ?, ?, ?)`,
		contentHash, model, embedding, time.Now().Unix()); err != nil {
		return fmt.Errorf("update embed_cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM embed_queue WHERE chunk_id = ?`, chunkID); err != nil {
		return fmt.Errorf("pop queue: %w", err)
	}
	return tx.Commit()
}

// PendingJob is one chunk waiting to be embedded.
type PendingJob struct {
	ChunkID     int64
	ContentHash []byte
	Content     string
}

// FetchPending returns up to `limit` queued jobs along with their content.
func (ix *Index) FetchPending(ctx context.Context, limit int) ([]PendingJob, error) {
	if limit <= 0 {
		limit = 16
	}
	rows, err := ix.db.QueryContext(ctx, `
		SELECT c.id, c.content_hash, c.content
		FROM embed_queue q
		JOIN chunks c ON c.id = q.chunk_id
		ORDER BY q.enqueued_at, q.chunk_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingJob
	for rows.Next() {
		var j PendingJob
		if err := rows.Scan(&j.ChunkID, &j.ContentHash, &j.Content); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// MarkJobFailed increments attempts and records the last error. The queue
// is not drained on failure — a retry happens on the next poll. The worker
// should apply backoff before retrying.
func (ix *Index) MarkJobFailed(ctx context.Context, chunkID int64, errMsg string) error {
	_, err := ix.db.ExecContext(ctx,
		`UPDATE embed_queue SET attempts = attempts + 1, last_error = ? WHERE chunk_id = ?`,
		errMsg, chunkID)
	return err
}

// InvalidateEmbeddingsForModel clears all stored embeddings when the user
// switches models (different dimensions would poison cosine similarity).
// Safe to call at startup if config.model != meta.embedder_model.
func (ix *Index) InvalidateEmbeddingsForModel(ctx context.Context, newModel string) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE chunks SET embedding = NULL, embed_model = NULL
		 WHERE embed_model IS NOT NULL AND embed_model <> ?`, newModel); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM embed_queue WHERE chunk_id IN (SELECT id FROM chunks WHERE embedding IS NULL)`); err != nil {
		return err
	}
	return tx.Commit()
}
