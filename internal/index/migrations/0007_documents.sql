-- v3.3: documents surface.
--
-- A parallel track to the symbol graph for files whose value is in
-- their (key, value) pairs rather than their callable structure:
-- i18n JSON, package.json dependencies, go.mod requires. Documents
-- have no refs and never participate in find_symbol /
-- get_references / get_neighborhood; they live behind their own
-- find_document_key query and a kind-aware FTS index.
--
-- files.document_kind is NULL for code files. For document-only
-- files (no symbol parser claims them) it carries the document
-- parser's Kind() — keeps doctor/stats groupings honest without
-- overloading files.language.

ALTER TABLE files ADD COLUMN document_kind TEXT;
CREATE INDEX idx_files_document_kind ON files(document_kind)
  WHERE document_kind IS NOT NULL;

CREATE TABLE documents (
    id      INTEGER PRIMARY KEY,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    kind    TEXT    NOT NULL,        -- i18n_json | package_json_deps | go_mod_requires
    key     TEXT    NOT NULL,        -- flattened key, e.g. "topbar.nav.back"
    value   TEXT    NOT NULL,
    line    INTEGER NOT NULL
);
CREATE INDEX idx_documents_file ON documents(file_id);
CREATE INDEX idx_documents_kind ON documents(kind);
CREATE INDEX idx_documents_key  ON documents(key);

CREATE VIRTUAL TABLE documents_fts USING fts5(
    key, value,
    content='documents',
    content_rowid='id',
    tokenize='trigram'
);

CREATE TRIGGER documents_ai AFTER INSERT ON documents BEGIN
    INSERT INTO documents_fts(rowid, key, value)
    VALUES (new.id, new.key, new.value);
END;
CREATE TRIGGER documents_ad AFTER DELETE ON documents BEGIN
    INSERT INTO documents_fts(documents_fts, rowid, key, value)
    VALUES ('delete', old.id, old.key, old.value);
END;
CREATE TRIGGER documents_au AFTER UPDATE ON documents BEGIN
    INSERT INTO documents_fts(documents_fts, rowid, key, value)
    VALUES ('delete', old.id, old.key, old.value);
    INSERT INTO documents_fts(rowid, key, value)
    VALUES (new.id, new.key, new.value);
END;
