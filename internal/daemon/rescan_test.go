package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/repo"
	"github.com/datata1/mycelium/internal/watch"
)

// fakeWatcher feeds a scripted event stream into the daemon's pump.
type fakeWatcher struct{ ch chan watch.Event }

func (f *fakeWatcher) Start(context.Context) error { return nil }
func (f *fakeWatcher) Events() <-chan watch.Event  { return f.ch }
func (f *fakeWatcher) Close() error                { return nil }

func testDaemonWithPipeline(t *testing.T) (*Daemon, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ix, err := index.Open(filepath.Join(root, ".mycelium", "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	reg := parser.NewRegistry()
	reg.Register(golang.New())
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   repo.NewWalker(root, []string{"**/*.go"}, nil, 0),
	}
	return &Daemon{Pipeline: p}, root
}

func TestPumpEvents_OverflowRequestsSingleRescan(t *testing.T) {
	t.Parallel()
	d, _ := testDaemonWithPipeline(t)
	fw := &fakeWatcher{ch: make(chan watch.Event, 8)}
	d.Watcher = fw

	rescanCh := make(chan string, 1)
	for i := 0; i < 5; i++ {
		fw.ch <- watch.Event{Overflow: true, Reason: "burst"}
	}
	close(fw.ch)
	d.pumpEvents(context.Background(), rescanCh)

	if len(rescanCh) != 1 {
		t.Fatalf("rescanCh holds %d entries, want 1 (storm must collapse)", len(rescanCh))
	}
	if reason := <-rescanCh; reason != "burst" {
		t.Errorf("reason = %q, want burst", reason)
	}
}

func TestPumpEvents_NormalEventsStillReachPipeline(t *testing.T) {
	t.Parallel()
	d, root := testDaemonWithPipeline(t)
	fw := &fakeWatcher{ch: make(chan watch.Event, 8)}
	d.Watcher = fw

	fw.ch <- watch.Event{RelPath: "a.go", AbsPath: filepath.Join(root, "a.go")}
	close(fw.ch)
	rescanCh := make(chan string, 1)
	d.pumpEvents(context.Background(), rescanCh)

	if len(rescanCh) != 0 {
		t.Errorf("normal event must not request a rescan")
	}
	var n int
	if err := d.Pipeline.Index.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("files = %d, want 1 (HandleChange indexed a.go)", n)
	}
}

func TestRescanLoop_RunsReconcilePerTrigger(t *testing.T) {
	t.Parallel()
	d, root := testDaemonWithPipeline(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rescanCh := make(chan string, 1)
	done := make(chan struct{})
	go func() { d.rescanLoop(ctx, rescanCh); close(done) }()

	rescanCh <- "test trigger"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		_ = d.Pipeline.Index.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n)
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var n int
	if err := d.Pipeline.Index.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("files = %d, want 1 (rescan indexed %s)", n, root)
	}
	cancel()
	<-done
}
