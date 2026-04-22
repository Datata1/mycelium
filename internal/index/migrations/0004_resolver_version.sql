-- v1.2: track which resolver produced each ref's dst_symbol_id.
--
-- 0 = textual (unique short-name fallback from v0.2)
-- 1 = go-types resolver (v1.2) — authoritative when present
-- 2+ = reserved for TS scope walker (v1.3), python scope walker (v1.3), etc.
--
-- On daemon start, we can re-resolve any ref whose resolver_version is below
-- the current algorithm's. v1.2 ships without automatic re-resolution; fresh
-- `myco index` runs pick up the new resolver naturally. Lazy re-resolution
-- lands once we need it (v1.3 when another language flips).
ALTER TABLE refs ADD COLUMN resolver_version INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_refs_resolver_version ON refs(resolver_version);
