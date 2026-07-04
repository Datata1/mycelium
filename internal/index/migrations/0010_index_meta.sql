-- Index-level metadata as key/value pairs. First consumer:
-- last_full_scan_at, written by the pipeline after a completed
-- reconcile (walk + upsert + prune), so freshness checks don't have
-- to infer scan time from MAX(files.last_indexed_at), which goes
-- stale on quiet repos even when scans run fine.
CREATE TABLE index_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
