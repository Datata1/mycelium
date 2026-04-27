package index

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestSkillFile_RoundTrip(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)

	h, err := ix.SkillFileHash(ctx, "packages/internal/query/SKILL.md")
	if err != nil {
		t.Fatalf("hash unknown: %v", err)
	}
	if h != "" {
		t.Errorf("unknown path should yield empty hash; got %q", h)
	}

	if err := ix.UpsertSkillFile(ctx, "packages/internal/query/SKILL.md", "abc123"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	h, err = ix.SkillFileHash(ctx, "packages/internal/query/SKILL.md")
	if err != nil {
		t.Fatalf("hash inserted: %v", err)
	}
	if h != "abc123" {
		t.Errorf("got %q want abc123", h)
	}

	first, err := ix.ListSkillFiles(ctx)
	if err != nil {
		t.Fatalf("list1: %v", err)
	}
	time.Sleep(time.Second + 10*time.Millisecond) // unix-second granularity
	if err := ix.UpsertSkillFile(ctx, "packages/internal/query/SKILL.md", "def456"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	second, err := ix.ListSkillFiles(ctx)
	if err != nil {
		t.Fatalf("list2: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected one row each pass; got %d/%d", len(first), len(second))
	}
	if !second[0].GeneratedAt.After(first[0].GeneratedAt) {
		t.Errorf("timestamp did not advance: first=%v second=%v",
			first[0].GeneratedAt, second[0].GeneratedAt)
	}
	if second[0].SkillHash != "def456" {
		t.Errorf("hash not updated; got %q want def456", second[0].SkillHash)
	}
}

func TestSkillFile_Prune(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)
	for _, p := range []string{"a/SKILL.md", "b/SKILL.md", "c/SKILL.md"} {
		if err := ix.UpsertSkillFile(ctx, p, p+"-hash"); err != nil {
			t.Fatalf("upsert %s: %v", p, err)
		}
	}
	if err := ix.PruneSkillFiles(ctx, []string{"a/SKILL.md", "c/SKILL.md"}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	rows, err := ix.ListSkillFiles(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 || rows[0].Path != "a/SKILL.md" || rows[1].Path != "c/SKILL.md" {
		t.Errorf("unexpected rows after prune: %+v", rows)
	}

	if err := ix.PruneSkillFiles(ctx, nil); err != nil {
		t.Fatalf("prune nil: %v", err)
	}
	rows, err = ix.ListSkillFiles(ctx)
	if err != nil {
		t.Fatalf("list2: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("nil prune dropped rows it shouldn't have: %+v", rows)
	}
}

func TestSkillFile_Delete(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)
	if err := ix.UpsertSkillFile(ctx, "x/SKILL.md", "h"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := ix.DeleteSkillFile(ctx, "x/SKILL.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	h, err := ix.SkillFileHash(ctx, "x/SKILL.md")
	if err != nil || h != "" {
		t.Errorf("expected no row after delete; got %q err=%v", h, err)
	}
}
