-- Add embedding + source text columns to chunks. We store chunk content
-- directly rather than reconstructing it on demand: it guarantees the
-- embedder sees the same bytes the chunker hashed, simplifies the worker,
-- and ~4KB per symbol × 50k symbols = ~200MB is acceptable for v0.4.
-- Future compaction: drop `content` once embedded, re-derive on model change.
ALTER TABLE chunks ADD COLUMN content TEXT NOT NULL DEFAULT '';

-- Little-endian packed float32 bytes; length = 4 * dimension. Null until
-- the chunk has been embedded.
ALTER TABLE chunks ADD COLUMN embedding BLOB;

-- Which model produced this chunk's embedding. Used to invalidate the whole
-- set if the user switches embedder models (dimensions may differ too).
ALTER TABLE chunks ADD COLUMN embed_model TEXT;

CREATE INDEX idx_chunks_embed_ready ON chunks(symbol_id) WHERE embedding IS NOT NULL;
