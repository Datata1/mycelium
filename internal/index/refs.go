package index

import (
	"context"
	"database/sql"
	"fmt"
)

// ResolveRefs fills dst_symbol_id on refs where a unique symbol with the
// matching qualified-or-short name now exists. It is safe to call repeatedly;
// only unresolved rows are touched.
//
// Resolution strategy (v0.2, intentionally modest):
//  1. Exact match on qualified name.
//  2. Otherwise unique match on short name within the symbols table.
//
// Ambiguous names (multiple symbols share a short name) stay unresolved; the
// textual dst_name still lets callers surface "textual-only" hits.
//
// This is an indexed update, not a scan — both join sides hit existing indexes
// on refs(dst_name) and symbols(name/qualified).
func (ix *Index) ResolveRefs(ctx context.Context, tx *sql.Tx) (int64, error) {
	// Exact qualified match.
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

	// Unique short-name match.
	res, err = tx.ExecContext(ctx, `
		UPDATE refs
		SET dst_symbol_id = (
		    SELECT id FROM symbols WHERE name = refs.dst_name LIMIT 1
		),
		    resolved = 1
		WHERE resolved = 0
		  AND (SELECT COUNT(*) FROM symbols WHERE name = refs.dst_name) = 1`)
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
