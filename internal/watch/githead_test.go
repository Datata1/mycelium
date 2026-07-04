package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rewriteAtomic mimics git's HEAD update: write a temp file, rename over.
func rewriteAtomic(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename: %v", err)
	}
}

func expectTick(t *testing.T, ticks <-chan struct{}, want bool, what string) {
	t.Helper()
	select {
	case <-ticks:
		if !want {
			t.Errorf("unexpected tick after %s", what)
		}
	case <-time.After(2 * time.Second):
		if want {
			t.Errorf("no tick within 2s after %s", what)
		}
	}
}

func TestHEADWatch_TicksOnHEADRewriteOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	head := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("seed HEAD: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks, err := StartHEADWatch(ctx, root, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the watch register

	// Non-HEAD churn must not tick.
	if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	expectTick(t, ticks, false, ".git/index write")

	// A checkout-style atomic rewrite must tick once.
	rewriteAtomic(t, head, "ref: refs/heads/feature\n")
	expectTick(t, ticks, true, "HEAD rewrite")
}

func TestHEADWatch_ResolvesWorktreeGitFile(t *testing.T) {
	t.Parallel()
	realGit := t.TempDir()
	if err := os.WriteFile(filepath.Join(realGit, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("seed HEAD: %v", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+realGit+"\n"), 0o644); err != nil {
		t.Fatalf("write .git link file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks, err := StartHEADWatch(ctx, root, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	rewriteAtomic(t, filepath.Join(realGit, "HEAD"), "ref: refs/heads/other\n")
	expectTick(t, ticks, true, "worktree HEAD rewrite")
}

func TestHEADWatch_NoGitDirIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := StartHEADWatch(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("expected an error for a directory without .git")
	}
}
