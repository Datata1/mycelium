package gitref_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/datata1/mycelium/internal/gitref"
)

// bootRepo creates a repo with one baseline commit containing a.go and b.go.
func bootRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "commit.gpgsign", "false")
	write(t, filepath.Join(dir, "a.go"), "package main\n\nfunc A() {}\n")
	write(t, filepath.Join(dir, "b.go"), "package main\n\nfunc B() {}\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-q", "-m", "baseline")
	return dir
}

func TestChangedSince_UncommittedVisible(t *testing.T) {
	dir := bootRepo(t)
	// One staged, one unstaged edit — both must show up against HEAD.
	write(t, filepath.Join(dir, "a.go"), "package main\n\nfunc A2() {}\n")
	run(t, dir, "git", "add", "a.go")
	write(t, filepath.Join(dir, "b.go"), "package main\n\nfunc B2() {}\n")

	base, paths, err := gitref.ChangedSince(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("ChangedSince: %v", err)
	}
	if base == "" {
		t.Error("expected non-empty base sha")
	}
	sort.Strings(paths)
	if !equal(paths, []string{"a.go", "b.go"}) {
		t.Errorf("paths = %v, want [a.go b.go]", paths)
	}
}

func TestChangedSince_CleanTree(t *testing.T) {
	dir := bootRepo(t)
	_, paths, err := gitref.ChangedSince(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("ChangedSince: %v", err)
	}
	if paths == nil || len(paths) != 0 {
		t.Errorf("expected non-nil empty slice, got %v", paths)
	}
}

func TestChangedSince_DeleteAndRenameSplit(t *testing.T) {
	dir := bootRepo(t)
	// Delete a.go; rename b.go → c.go. --no-renames must list b.go (D)
	// and c.go (A) separately so the old path's symbols get diffed.
	run(t, dir, "git", "rm", "-q", "a.go")
	run(t, dir, "git", "mv", "b.go", "c.go")

	_, paths, err := gitref.ChangedSince(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("ChangedSince: %v", err)
	}
	sort.Strings(paths)
	if !equal(paths, []string{"a.go", "b.go", "c.go"}) {
		t.Errorf("paths = %v, want [a.go b.go c.go]", paths)
	}
}

func TestChangedSince_CommittedBranchDiff(t *testing.T) {
	dir := bootRepo(t)
	write(t, filepath.Join(dir, "a.go"), "package main\n\nfunc A3() {}\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-q", "-m", "second")

	base, paths, err := gitref.ChangedSince(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatalf("ChangedSince: %v", err)
	}
	if !equal(paths, []string{"a.go"}) {
		t.Errorf("paths = %v, want [a.go]", paths)
	}
	// Base must resolve to HEAD~1's sha.
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD~1").Output()
	if want := strings.TrimSpace(string(out)); base != want {
		t.Errorf("base = %q, want %q", base, want)
	}
}

func TestChangedSince_EmptyRef(t *testing.T) {
	if _, _, err := gitref.ChangedSince(context.Background(), t.TempDir(), ""); err == nil {
		t.Error("expected error for empty ref")
	}
}

func TestShowAtCommit(t *testing.T) {
	dir := bootRepo(t)
	content, ok, err := gitref.ShowAtCommit(context.Background(), dir, "HEAD", "a.go")
	if err != nil || !ok {
		t.Fatalf("ShowAtCommit: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(content), "func A()") {
		t.Errorf("unexpected content: %q", content)
	}
}

func TestShowAtCommit_MissingAtBase(t *testing.T) {
	dir := bootRepo(t)
	write(t, filepath.Join(dir, "new.go"), "package main\n")
	run(t, dir, "git", "add", "new.go")

	_, ok, err := gitref.ShowAtCommit(context.Background(), dir, "HEAD", "new.go")
	if err != nil {
		t.Fatalf("ShowAtCommit: %v", err)
	}
	if ok {
		t.Error("expected ok=false for path missing at commit")
	}
}

func TestShowAtCommit_DeletedFileStillReadable(t *testing.T) {
	dir := bootRepo(t)
	if err := os.Remove(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	content, ok, err := gitref.ShowAtCommit(context.Background(), dir, "HEAD", "a.go")
	if err != nil || !ok {
		t.Fatalf("ShowAtCommit after delete: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(content), "func A()") {
		t.Errorf("unexpected content: %q", content)
	}
}
