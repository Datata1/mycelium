package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
	"github.com/jdwiederstein/mycelium/internal/watch"
)

// fakeWatcher is a hand-pushable Watcher implementation. Tests call
// Push() to feed events; Close is idempotent because the daemon's
// shutdown goroutine also calls it on context cancel.
type fakeWatcher struct {
	events chan watch.Event
	closed bool
	mu     sync.Mutex
}

func (f *fakeWatcher) Start(ctx context.Context) error { return nil }
func (f *fakeWatcher) Events() <-chan watch.Event      { return f.events }
func (f *fakeWatcher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	close(f.events)
	return nil
}
func (f *fakeWatcher) Push(ev watch.Event) { f.events <- ev }

// TestDaemon_SkillsRegen_BatchesAcrossDebounce is the v2.5 invariant
// the daemon promises: a burst of file events resolves to exactly one
// SkillsRegen call once the channel has been idle for SkillsDebounce.
// Two events in two different packages should arrive together.
func TestDaemon_SkillsRegen_BatchesAcrossDebounce(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(filepath.Join(root, "a", "a.go"), "package a\nfunc A() {}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writeFile(filepath.Join(root, "b", "b.go"), "package b\nfunc B() {}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	ix, err := index.Open(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ix.Close()
	reg := parser.NewRegistry()
	reg.Register(golang.New())
	w := repo.NewWalker(root, nil, nil, 0)
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: w}
	if _, err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	wat := &fakeWatcher{events: make(chan watch.Event, 8)}
	socket := filepath.Join(root, ".mycelium", "daemon.sock")

	var (
		mu    sync.Mutex
		calls [][]string
	)
	d := &Daemon{
		Pipeline:       p,
		Reader:         query.NewReader(ix.DB()),
		Watcher:        wat,
		Socket:         socket,
		RepoRoot:       root,
		SkillsDebounce: 50 * time.Millisecond,
		SkillsRegen: func(_ context.Context, pkgs []string) error {
			mu.Lock()
			defer mu.Unlock()
			cp := append([]string(nil), pkgs...)
			sort.Strings(cp)
			calls = append(calls, cp)
			return nil
		},
		Logger: silentLogger{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	// Give Run a moment to spin up the goroutines + listen on the socket.
	time.Sleep(20 * time.Millisecond)

	// Two events in the same burst → one batch with both packages.
	wat.Push(watch.Event{RelPath: "a/a.go", AbsPath: filepath.Join(root, "a", "a.go")})
	wat.Push(watch.Event{RelPath: "b/b.go", AbsPath: filepath.Join(root, "b", "b.go")})
	time.Sleep(150 * time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 SkillsRegen call, got %d: %+v", len(calls), calls)
	}
	got := calls[0]
	want := []string{"a", "b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got packages %v, want %v", got, want)
	}
}

// TestDaemon_SkillsRegen_NotInvokedWhenNil verifies the dispatch path
// stays cold when SkillsRegen is nil — users who never compiled the
// tree shouldn't pay the bookkeeping cost.
func TestDaemon_SkillsRegen_NotInvokedWhenNil(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(filepath.Join(root, "x.go"), "package x\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	ix, err := index.Open(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ix.Close()
	reg := parser.NewRegistry()
	reg.Register(golang.New())
	w := repo.NewWalker(root, nil, nil, 0)
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: w}
	if _, err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	wat := &fakeWatcher{events: make(chan watch.Event, 4)}
	d := &Daemon{
		Pipeline: p,
		Reader:   query.NewReader(ix.DB()),
		Watcher:  wat,
		Socket:   filepath.Join(root, ".mycelium", "daemon.sock"),
		RepoRoot: root,
		Logger:   silentLogger{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	wat.Push(watch.Event{RelPath: "x.go", AbsPath: filepath.Join(root, "x.go")})
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done
	// No assertions on calls — there's nothing to count. The success
	// criterion is "doesn't deadlock or panic with SkillsRegen=nil".
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

type silentLogger struct{}

func (silentLogger) Printf(format string, args ...any) {}
