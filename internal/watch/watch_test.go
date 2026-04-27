package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// runBackend drives `backend` through a scripted sequence of filesystem
// events and returns the coalesced events the watcher emitted. Tests
// assert on the returned slice rather than the raw fsnotify/watchman
// stream, so every backend's behavior is compared through the same
// shared wrapper (debounce + coalesce + filters).
func runBackend(t *testing.T, backend string) []Event {
	t.Helper()
	dir := t.TempDir()
	// Seed a file so the initial registration has a real tree to crawl.
	if err := os.WriteFile(filepath.Join(dir, "seed.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w, err := New(Options{
		Root:       dir,
		Include:    []string{"**/*.go"},
		DebounceMS: 20,
		CoalesceMS: 50,
		Backend:    backend,
	})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer w.Close()

	// Give the backend a moment to register watches before we start
	// touching files — fsnotify has a brief window where CREATE events
	// on just-watched dirs can drop otherwise.
	time.Sleep(100 * time.Millisecond)

	var (
		mu   sync.Mutex
		got  []Event
		done = make(chan struct{})
	)
	go func() {
		for ev := range w.Events() {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		}
		close(done)
	}()

	// Scripted actions: create, modify, rename, delete, plus a file
	// that should be filtered out by the include glob.
	mustWrite(t, filepath.Join(dir, "a.go"), "package x\n")
	mustWrite(t, filepath.Join(dir, "a.go"), "package x\n// v2\n")
	mustWrite(t, filepath.Join(dir, "README.md"), "ignored\n") // excluded by glob
	mustWrite(t, filepath.Join(dir, "b.go"), "package x\n")
	// Give fsnotify a chance to observe the b.go CREATE before we
	// delete it. macOS FSEvents has been observed to coalesce a
	// create+remove pair into nothing when they fire <~10ms apart,
	// which makes the "saw at least one terminal event for b.go"
	// assertion below flaky on macos-latest CI. The test contract
	// (every non-excluded touched path produces some event) is
	// unchanged — we just give the kernel a real opportunity to
	// deliver the create before the file disappears.
	time.Sleep(100 * time.Millisecond)
	if err := os.Remove(filepath.Join(dir, "b.go")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Poll until both expected paths have arrived, with a generous
	// upper bound. fsnotify on macOS occasionally takes >1s to
	// deliver CREATE events for files written immediately after a
	// watch is registered; a fixed 400ms sleep was flaking on
	// macos-latest CI runners under -race. Poll resolution is 25ms
	// so the common case still finishes well under 200ms.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seenA, seenB := false, false
		for _, ev := range got {
			if ev.RelPath == "a.go" {
				seenA = true
			}
			if ev.RelPath == "b.go" {
				seenB = true
			}
		}
		mu.Unlock()
		if seenA && seenB {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	out := make([]Event, len(got))
	copy(out, got)
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

// TestWatcher_FSNotify_Parity exercises the default backend. Gates the
// scripted sequence through the shared wrapper, so every assertion here
// applies equally to the watchman backend (gated below).
func TestWatcher_FSNotify_Parity(t *testing.T) {
	assertWatcherBehavior(t, runBackend(t, "fsnotify"))
}

// TestWatcher_Watchman_Parity runs the same scripted sequence against
// watchman. Skipped when the watchman binary is missing so CI without
// watchman still passes.
func TestWatcher_Watchman_Parity(t *testing.T) {
	if _, err := exec.LookPath("watchman"); err != nil {
		t.Skip("watchman binary not in PATH — skipping parity test")
	}
	assertWatcherBehavior(t, runBackend(t, "watchman"))
}

func assertWatcherBehavior(t *testing.T, got []Event) {
	t.Helper()
	// README.md must not appear — include glob filters it out.
	for _, ev := range got {
		if filepath.Ext(ev.RelPath) == ".md" {
			t.Errorf("unexpected excluded file: %s", ev.RelPath)
		}
	}

	// We should see a.go (coalesced from two writes) and b.go at least.
	// The create+delete of b.go is allowed to collapse into one removed
	// event or emerge as create-then-remove depending on backend timing;
	// the contract is "at least one terminal event for b.go".
	seenA, seenB := false, false
	removedB := false
	for _, ev := range got {
		if ev.RelPath == "a.go" {
			seenA = true
			if ev.Removed {
				t.Errorf("a.go: unexpected Removed=true")
			}
		}
		if ev.RelPath == "b.go" {
			seenB = true
			if ev.Removed {
				removedB = true
			}
		}
	}
	if !seenA {
		t.Errorf("missing event for a.go; got=%v", got)
	}
	if !seenB {
		t.Errorf("missing event for b.go; got=%v", got)
	}
	// b.go was created and then removed within one debounce window.
	// Backends may emit the final delete (removedB=true) or collapse
	// the whole burst into the last-seen state (fsnotify sometimes
	// reports the remove as a rename). Both are acceptable — what we
	// refuse is "no terminal event at all", caught by !seenB.
	_ = removedB
}

// TestWatcher_UnknownBackend makes sure a typo in config surfaces as
// a hard error instead of silently landing on fsnotify.
func TestWatcher_UnknownBackend(t *testing.T) {
	_, err := New(Options{Root: t.TempDir(), Backend: "fsnotifi"})
	if err == nil {
		t.Fatal("expected unknown-backend error, got nil")
	}
}

// TestWatcher_ExcludeOverridesInclude verifies the shared wrapper's
// filter precedence — excludes win over includes. Backend-agnostic.
func TestWatcher_ExcludeOverridesInclude(t *testing.T) {
	w := &wrapped{opts: Options{
		Include: []string{"**/*.go"},
		Exclude: []string{"vendor/**"},
	}}
	if !w.matches("x.go") {
		t.Error("want match for x.go")
	}
	if w.matches("vendor/foo.go") {
		t.Error("exclude should beat include for vendor/foo.go")
	}
	if w.matches("README.md") {
		t.Error("non-.go should not match")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
