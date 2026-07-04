package index

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func openTestIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	return ix
}

func insertFileRows(t *testing.T, ix *Index, n int) {
	t.Helper()
	ctx := context.Background()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO files(path, language, size_bytes, mtime_ns, content_hash, parse_hash, last_indexed_at)
			VALUES(?, 'go', 1, 1, ?, ?, 1)`,
			fmt.Sprintf("pkg/file_%04d.go", i), []byte{byte(i)}, []byte{byte(i)}); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// The delete runs in chunks of 500; 1200 stale rows exercise a full chunk,
// a second full chunk, and a partial tail.
func TestPruneFilesExcept_ChunkedDelete(t *testing.T) {
	t.Parallel()
	ix := openTestIndex(t)
	insertFileRows(t, ix, 1250)

	keep := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		keep[fmt.Sprintf("pkg/file_%04d.go", i)] = struct{}{}
	}
	pruned, err := ix.PruneFilesExcept(context.Background(), keep)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 1200 {
		t.Errorf("pruned = %d, want 1200", pruned)
	}
	var left int
	if err := ix.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 50 {
		t.Errorf("rows left = %d, want 50", left)
	}
}

func TestPruneFilesExcept_NothingStale(t *testing.T) {
	t.Parallel()
	ix := openTestIndex(t)
	insertFileRows(t, ix, 10)

	keep := map[string]struct{}{}
	for i := 0; i < 10; i++ {
		keep[fmt.Sprintf("pkg/file_%04d.go", i)] = struct{}{}
	}
	pruned, err := ix.PruneFilesExcept(context.Background(), keep)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0", pruned)
	}
}

func TestSetMeta_Upserts(t *testing.T) {
	t.Parallel()
	ix := openTestIndex(t)
	ctx := context.Background()

	if err := ix.SetMeta(ctx, "last_full_scan_at", "100"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := ix.SetMeta(ctx, "last_full_scan_at", "200"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	var v string
	if err := ix.db.QueryRow(`SELECT value FROM index_meta WHERE key = 'last_full_scan_at'`).Scan(&v); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if v != "200" {
		t.Errorf("value = %q, want 200", v)
	}
}
