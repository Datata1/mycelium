package watch

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/jdwiederstein/mycelium/internal/repo"
)

// Event is a coalesced change notification for a single repo-relative path.
// The watcher guarantees that bursts of fs events for the same file collapse
// into at most one Event per (debounce window + coalesce window).
type Event struct {
	RelPath string
	AbsPath string
	Removed bool // true if the file no longer exists
}

// Watcher wraps fsnotify with per-file debounce and a simple output channel.
// It is driven entirely by goroutines; callers Start it, consume Events(),
// and Close it when done.
type Watcher struct {
	root       string
	inc, exc   []string
	maxKB      int
	skipDirs   map[string]bool
	debounceMS int

	fsw     *fsnotify.Watcher
	out     chan Event
	pending map[string]*pendingEvent
	mu      sync.Mutex
	done    chan struct{}
	closeOnce sync.Once
}

type pendingEvent struct {
	timer   *time.Timer
	removed bool
}

// New creates a Watcher rooted at the given path with the given include/exclude
// globs. Only files that would pass the walker's filters are emitted.
func New(root string, include, exclude []string, maxFileSizeKB, debounceMS int) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("new fsnotify watcher: %w", err)
	}
	return &Watcher{
		root:       root,
		inc:        include,
		exc:        exclude,
		maxKB:      maxFileSizeKB,
		skipDirs:   map[string]bool{".git": true, ".mycelium": true},
		debounceMS: debounceMS,
		fsw:        fsw,
		out:        make(chan Event, 256),
		pending:    map[string]*pendingEvent{},
		done:       make(chan struct{}),
	}, nil
}

// Events returns the output channel. Consumers receive coalesced events here.
func (w *Watcher) Events() <-chan Event { return w.out }

// Start registers every directory under root with fsnotify and launches the
// background pump. It returns after the initial registration completes.
// Context cancellation triggers Close.
func (w *Watcher) Start(ctx context.Context) error {
	if err := w.addTree(w.root); err != nil {
		return fmt.Errorf("register watches: %w", err)
	}
	go w.pump(ctx)
	return nil
}

// Close tears down the watcher. Safe to call multiple times.
func (w *Watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)
		err = w.fsw.Close()
	})
	return err
}

func (w *Watcher) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // best-effort
		}
		if d.IsDir() {
			if w.skipDirs[d.Name()] && path != root {
				return filepath.SkipDir
			}
			if err := w.fsw.Add(path); err != nil {
				// Don't fail the whole registration for one bad dir.
				return nil
			}
		}
		return nil
	})
}

func (w *Watcher) pump(ctx context.Context) {
	defer close(w.out)
	for {
		select {
		case <-ctx.Done():
			_ = w.Close()
			return
		case <-w.done:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			_ = err // log via telemetry once we wire it in
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	// If a directory was created, start watching it so nested files also emit
	// events. Best-effort; failures are silent.
	if ev.Has(fsnotify.Create) {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			name := filepath.Base(ev.Name)
			if !w.skipDirs[name] {
				_ = w.fsw.Add(ev.Name)
			}
			return
		}
	}
	rel, err := filepath.Rel(w.root, ev.Name)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if w.skipDir(rel) {
		return
	}
	if !w.includes(rel) || w.excludes(rel) {
		return
	}
	removed := ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)
	w.schedule(rel, ev.Name, removed)
}

func (w *Watcher) schedule(rel, abs string, removed bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.pending[rel]; ok {
		p.timer.Stop()
		// If any event in the burst was a removal, keep that signal; a later
		// create will overwrite it back to false.
		if removed {
			p.removed = true
		}
		if !removed {
			p.removed = false
		}
		p.timer.Reset(time.Duration(w.debounceMS) * time.Millisecond)
		return
	}
	pe := &pendingEvent{removed: removed}
	pe.timer = time.AfterFunc(time.Duration(w.debounceMS)*time.Millisecond, func() {
		w.mu.Lock()
		state := w.pending[rel]
		delete(w.pending, rel)
		w.mu.Unlock()
		if state == nil {
			return
		}
		select {
		case w.out <- Event{RelPath: rel, AbsPath: abs, Removed: state.removed}:
		case <-w.done:
		}
	})
	w.pending[rel] = pe
}

func (w *Watcher) skipDir(rel string) bool {
	for _, part := range splitPath(rel) {
		if w.skipDirs[part] {
			return true
		}
	}
	return false
}

func (w *Watcher) includes(rel string) bool {
	if len(w.inc) == 0 {
		return true
	}
	for _, pat := range w.inc {
		if repo.DoublestarMatch(pat, rel) {
			return true
		}
	}
	return false
}

func (w *Watcher) excludes(rel string) bool {
	for _, pat := range w.exc {
		if repo.DoublestarMatch(pat, rel) {
			return true
		}
	}
	return false
}

func splitPath(p string) []string {
	var out []string
	for len(p) > 0 {
		i := 0
		for i < len(p) && p[i] != '/' {
			i++
		}
		out = append(out, p[:i])
		if i == len(p) {
			break
		}
		p = p[i+1:]
	}
	return out
}
