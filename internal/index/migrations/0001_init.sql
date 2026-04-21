-- Mycelium schema v1.
-- Applied once at database creation. Subsequent changes go in new numbered files.

PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;

-- ----------------------------------------------------------------------------
-- files: one row per indexed file.
-- content_hash drives "did this file change?"; parse_hash drives "did parsing output change?"
-- ----------------------------------------------------------------------------
CREATE TABLE files (
    id              INTEGER PRIMARY KEY,
    path            TEXT    NOT NULL UNIQUE,       -- repo-relative, forward slashes
    language        TEXT    NOT NULL,
    size_bytes      INTEGER NOT NULL,
    mtime_ns        INTEGER NOT NULL,
    content_hash    BLOB    NOT NULL,              -- blake3 of file bytes
    parse_hash      BLOB    NOT NULL,              -- hash of parser output
    last_indexed_at INTEGER NOT NULL
);
CREATE INDEX idx_files_language ON files(language);

-- ----------------------------------------------------------------------------
-- symbols: definitions (function, method, class, type, var, const, interface).
-- symbol_hash = hash of signature + body; drives re-embed decisions.
-- parent_id lets methods point at their enclosing type.
-- ----------------------------------------------------------------------------
CREATE TABLE symbols (
    id          INTEGER PRIMARY KEY,
    file_id     INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    qualified   TEXT    NOT NULL,                  -- e.g. "pkg.Type.Method"
    kind        TEXT    NOT NULL,                  -- function | method | class | type | var | const | interface
    start_line  INTEGER NOT NULL,
    start_col   INTEGER NOT NULL,
    end_line    INTEGER NOT NULL,
    end_col     INTEGER NOT NULL,
    signature   TEXT,
    docstring   TEXT,
    visibility  TEXT,                              -- public | private | package
    parent_id   INTEGER REFERENCES symbols(id) ON DELETE CASCADE,
    symbol_hash BLOB    NOT NULL
);
CREATE INDEX idx_symbols_name      ON symbols(name);
CREATE INDEX idx_symbols_qualified ON symbols(qualified);
CREATE INDEX idx_symbols_file      ON symbols(file_id);
CREATE INDEX idx_symbols_kind      ON symbols(kind);

-- ----------------------------------------------------------------------------
-- FTS5 over name + qualified + docstring for fuzzy symbol lookup.
-- Trigram tokenizer gives sensible partial / misspelled matches.
-- ----------------------------------------------------------------------------
CREATE VIRTUAL TABLE symbols_fts USING fts5(
    name,
    qualified,
    docstring,
    content='symbols',
    content_rowid='id',
    tokenize='trigram'
);

-- Keep FTS index in sync with the symbols table.
CREATE TRIGGER symbols_ai AFTER INSERT ON symbols BEGIN
    INSERT INTO symbols_fts(rowid, name, qualified, docstring)
    VALUES (new.id, new.name, new.qualified, new.docstring);
END;
CREATE TRIGGER symbols_ad AFTER DELETE ON symbols BEGIN
    INSERT INTO symbols_fts(symbols_fts, rowid, name, qualified, docstring)
    VALUES ('delete', old.id, old.name, old.qualified, old.docstring);
END;
CREATE TRIGGER symbols_au AFTER UPDATE ON symbols BEGIN
    INSERT INTO symbols_fts(symbols_fts, rowid, name, qualified, docstring)
    VALUES ('delete', old.id, old.name, old.qualified, old.docstring);
    INSERT INTO symbols_fts(rowid, name, qualified, docstring)
    VALUES (new.id, new.name, new.qualified, new.docstring);
END;

-- ----------------------------------------------------------------------------
-- refs: cross-symbol references (calls, imports, type uses, inheritance).
-- dst_name is always present (textual target); dst_symbol_id filled by the
-- resolution pass when a matching symbol exists. resolved = 0 until then.
-- ----------------------------------------------------------------------------
CREATE TABLE refs (
    id            INTEGER PRIMARY KEY,
    src_file_id   INTEGER NOT NULL REFERENCES files(id)   ON DELETE CASCADE,
    src_symbol_id INTEGER          REFERENCES symbols(id) ON DELETE CASCADE,
    dst_symbol_id INTEGER          REFERENCES symbols(id) ON DELETE SET NULL,
    dst_name      TEXT    NOT NULL,                -- full textual target, e.g. "r.FindSymbol" or "fmt.Println"
    dst_short     TEXT    NOT NULL,                -- last segment after final dot, e.g. "FindSymbol"
    kind          TEXT    NOT NULL,                -- call | import | type_ref | inherit
    line          INTEGER NOT NULL,
    col           INTEGER NOT NULL,
    resolved      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_refs_src       ON refs(src_symbol_id);
CREATE INDEX idx_refs_dst       ON refs(dst_symbol_id);
CREATE INDEX idx_refs_dst_name  ON refs(dst_name);
CREATE INDEX idx_refs_dst_short ON refs(dst_short);
CREATE INDEX idx_refs_file      ON refs(src_file_id);
CREATE INDEX idx_refs_unres     ON refs(resolved) WHERE resolved = 0;

-- ----------------------------------------------------------------------------
-- chunks: embeddable units. One per symbol, plus fallback window chunks for
-- files without extracted symbols (SQL, Markdown, config, etc).
-- content_hash is the dedup key into embed_cache.
-- ----------------------------------------------------------------------------
CREATE TABLE chunks (
    id           INTEGER PRIMARY KEY,
    file_id      INTEGER NOT NULL REFERENCES files(id)   ON DELETE CASCADE,
    symbol_id    INTEGER          REFERENCES symbols(id) ON DELETE CASCADE,
    kind         TEXT    NOT NULL,                 -- symbol | window
    start_line   INTEGER NOT NULL,
    end_line     INTEGER NOT NULL,
    content_hash BLOB    NOT NULL,
    tokens       INTEGER
);
CREATE INDEX idx_chunks_symbol ON chunks(symbol_id);
CREATE INDEX idx_chunks_hash   ON chunks(content_hash);
CREATE INDEX idx_chunks_file   ON chunks(file_id);

-- ----------------------------------------------------------------------------
-- vss_chunks: vector index. Created lazily by the application when the
-- sqlite-vec extension is loaded AND an embedder is configured.
-- Intentionally omitted from this migration so the base DB works without
-- sqlite-vec. See index/vss.go for the create-if-missing logic.
-- Expected shape:
--   CREATE VIRTUAL TABLE vss_chunks USING vec0(embedding float[<dim>]);
--   vss_chunks.rowid == chunks.id
-- ----------------------------------------------------------------------------

-- ----------------------------------------------------------------------------
-- embed_cache: content_hash -> embedding. Survives reindex, so renames and
-- moves cost zero API calls.
-- ----------------------------------------------------------------------------
CREATE TABLE embed_cache (
    content_hash BLOB PRIMARY KEY,
    model        TEXT    NOT NULL,
    embedding    BLOB    NOT NULL,
    created_at   INTEGER NOT NULL
);

-- ----------------------------------------------------------------------------
-- embed_queue: pending work. Persisted so a daemon restart resumes cleanly.
-- ----------------------------------------------------------------------------
CREATE TABLE embed_queue (
    chunk_id    INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    enqueued_at INTEGER NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT
);

-- ----------------------------------------------------------------------------
-- meta: simple key/value for schema version, embedder model, dim, etc.
-- ----------------------------------------------------------------------------
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO meta(key, value) VALUES
    ('schema_version', '1'),
    ('created_at', strftime('%s','now'));
