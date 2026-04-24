package gitref_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jdwiederstein/mycelium/internal/gitref"
)

func TestResolveSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()

	// Bootstrap a repo with two commits: an initial baseline and a
	// change set we'll ask `git diff` to recover.
	run(t, dir, "git", "init", "-q", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "commit.gpgsign", "false")

	write(t, filepath.Join(dir, "keep.txt"), "untouched\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-q", "-m", "baseline")

	write(t, filepath.Join(dir, "one.go"), "package main\n")
	write(t, filepath.Join(dir, "two.go"), "package main\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-q", "-m", "change")

	paths, err := gitref.ResolveSince(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sort.Strings(paths)
	want := []string{"one.go", "two.go"}
	if !equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestResolveSince_EmptyRef(t *testing.T) {
	if _, err := gitref.ResolveSince(context.Background(), t.TempDir(), ""); err == nil {
		t.Error("expected error for empty ref; got nil")
	}
}

func TestResolveSince_UnknownRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	// No commits yet; any ref should fail.
	_, err := gitref.ResolveSince(context.Background(), dir, "no-such-ref")
	if err == nil {
		t.Error("expected error for unknown ref; got nil")
	}
}

func TestResolveSince_NoChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "commit.gpgsign", "false")
	write(t, filepath.Join(dir, "only.txt"), "x\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-q", "-m", "only")

	// HEAD...HEAD = zero diff; must be a non-nil empty slice so the
	// reader sees the zero-row sentinel instead of "no filter".
	paths, err := gitref.ResolveSince(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if paths == nil {
		t.Error("paths = nil; want non-nil empty slice for the no-changes case")
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v; want empty", paths)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
