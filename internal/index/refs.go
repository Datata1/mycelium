package index

import (
	"context"
	"database/sql"
	"fmt"
)

// ResolveRefs fills dst_symbol_id on refs where a qualified or (carefully)
// short-named symbol exists. Safe to call repeatedly; only unresolved rows
// are touched.
//
// Resolution strategy (v1.2):
//  1. Exact match on qualified name. Applies to ALL resolver versions —
//     this is how type-resolved refs (v1) land against parser-produced
//     symbols in the same shortpkg.Receiver.Method form.
//  2. Unique short-name fallback — ONLY for resolver_version=0 rows.
//     Type-resolved refs that the Go resolver deliberately left unresolved
//     (builtins, external packages, calls whose receiver type was erased)
//     must not get wishful-thinking short-name matches: that was the
//     self-loop generator in v1.1 and earlier.
//
// Ambiguous names (multiple symbols share a short name) stay unresolved;
// the textual dst_name still surfaces "textual-only" hits to callers.
//
// Both passes are indexed updates, not scans.
func (ix *Index) ResolveRefs(ctx context.Context, tx *sql.Tx) (int64, error) {
	// Pass 1: exact qualified match. Applies regardless of resolver version.
	res, err := tx.ExecContext(ctx, `
		UPDATE refs
		SET dst_symbol_id = (
		    SELECT id FROM symbols WHERE qualified = refs.dst_name LIMIT 1
		),
		    resolved = 1
		WHERE resolved = 0
		  AND EXISTS (SELECT 1 FROM symbols WHERE qualified = refs.dst_name)`)
	if err != nil {
		return 0, fmt.Errorf("resolve by qualified: %w", err)
	}
	nq, _ := res.RowsAffected()

	// Pass 2: unique short-name fallback — resolver_version=0 only.
	// Keeps the v1.1 behavior for languages that don't yet have a
	// type-aware resolver (TS, Python — both at 0 until v1.3).
	res, err = tx.ExecContext(ctx, `
		UPDATE refs
		SET dst_symbol_id = (
		    SELECT id FROM symbols WHERE name = refs.dst_short LIMIT 1
		),
		    resolved = 1
		WHERE resolved = 0
		  AND resolver_version = 0
		  AND (SELECT COUNT(*) FROM symbols WHERE name = refs.dst_short) = 1`)
	if err != nil {
		return 0, fmt.Errorf("resolve by short name: %w", err)
	}
	nn, _ := res.RowsAffected()

	return nq + nn, nil
}

// SyncResolvedFlag repairs the resolved column after FK cascades set
// dst_symbol_id to NULL (refs.dst_symbol_id has ON DELETE SET NULL). This
// keeps the invariant: resolved = 1 iff dst_symbol_id IS NOT NULL.
func (ix *Index) SyncResolvedFlag(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE refs
		SET resolved = 0
		WHERE resolved = 1 AND dst_symbol_id IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("sync resolved flag: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
