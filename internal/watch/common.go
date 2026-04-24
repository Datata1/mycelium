package watch

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/jdwiederstein/mycelium/internal/repo"
)

// wrapped is the default public Watcher implementation: it owns a
// rawSource (fsnotify or watchman) and applies the shared policy —
// include/exclude globs, MaxFileSizeKB, per-file debounce,
// cross-file coalesce — before emitting on the public channel.
//
// The pump goroutine is the only writer on `out`; debounce and
// coalesce timers signal pump through internal channels rather than
// writing directly, so teardown has no send-on-closed-channel race.
type wrapped struct {
	opts Options
	src  rawSource

	out       chan Event
	done      chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	pending map[string]*pendingEvent

	// Signal channels pumped by pump(). Both debounced events and
	// coalesce-flush ticks flow through here so pump owns all writes
	// to `out` and can close it safely.
	emitCh  chan Event
	flushCh chan struct{}

	coalMu    sync.Mutex
	coalBatch []Event
	coalTimer *time.Timer
}

type pendingEvent struct {
	timer   *time.Timer
	removed bool
	abs     string
}

// skipDirs are never watched regardless of config — .git / .mycelium
// would otherwise generate enormous noise.
var skipDirs = map[string]bool{".git": true, ".mycelium": true}

func newWrapped(opts Options, src rawSource) *wrapped {
	return &wrapped{
		opts:    opts,
		src:     src,
		out:     make(chan Event, 256),
		done:    make(chan struct{}),
		pending: map[string]*pendingEvent{},
		emitCh:  make(chan Event, 256),
		flushCh: make(chan struct{}, 4),
	}
}

func (w *wrapped) Events() <-chan Event { return w.out }

func (w *wrapped) Start(ctx context.Context) error {
	if err := w.src.start(ctx); err != nil {
		return err
	}
	go w.pump(ctx)
	return nil
}

func (w *wrapped) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)
		err = w.src.close()
	})
	return err
}

// pump owns all writes on `out`. It drains raw events from the backend,
// debounced events from emitCh, and coalesce-flush signals from flushCh.
// Terminates when the raw channel closes, ctx is cancelled, or Close()
// is called — flushing any buffered coalesce batch before exiting.
func (w *wrapped) pump(ctx context.Context) {
	defer close(w.out)
	raw := w.src.events()
	for {
		select {
		case <-ctx.Done():
			_ = w.Close()
			w.flushCoalesceBatch()
			return
		case <-w.done:
			w.flushCoalesceBatch()
			return
		case ev, ok := <-raw:
			if !ok {
				w.flushCoalesceBatch()
				return
			}
			w.handle(ev)
		case ev := <-w.emitCh:
			w.emit(ev)
		case <-w.flushCh:
			w.flushCoalesceBatch()
		}
	}
}

// handle applies the shared policy to one raw event and (if it
// survives) schedules it on the debounce timer.
func (w *wrapped) handle(ev rawEvent) {
	if w.shouldSkipPath(ev.RelPath) {
		return
	}
	if !w.matches(ev.RelPath) {
		return
	}
	// Size cap applies only when the file still exists on disk.
	// Removed events always pass through so the index can catch up.
	if !ev.Removed && w.opts.MaxFileSizeKB > 0 && ev.AbsPath != "" {
		if info, err := os.Stat(ev.AbsPath); err == nil {
			if info.Size() > int64(w.opts.MaxFileSizeKB)*1024 {
				return
			}
		}
	}
	w.schedule(ev)
}

// schedule debounces per-file. The last-seen Removed flag wins (so a
// delete-then-create burst settles as "exists"); that matches the
// behavior users see on editor atomic-save sequences.
func (w *wrapped) schedule(ev rawEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delay := time.Duration(w.opts.DebounceMS) * time.Millisecond
	if delay < 0 {
		delay = 0
	}
	if p, ok := w.pending[ev.RelPath]; ok {
		p.timer.Stop()
		p.removed = ev.Removed
		p.abs = ev.AbsPath
		p.timer.Reset(delay)
		return
	}
	pe := &pendingEvent{removed: ev.Removed, abs: ev.AbsPath}
	rel := ev.RelPath
	pe.timer = time.AfterFunc(delay, func() {
		w.mu.Lock()
		state := w.pending[rel]
		delete(w.pending, rel)
		w.mu.Unlock()
		if state == nil {
			return
		}
		// Hand off to pump via emitCh. If we're shutting down, pump
		// has already drained or is about to — drop the event.
		select {
		case w.emitCh <- Event{RelPath: rel, AbsPath: state.abs, Removed: state.removed}:
		case <-w.done:
		}
	})
	w.pending[ev.RelPath] = pe
}

// emit runs in pump: either flushes the event immediately
// (CoalesceMS <= 0) or buffers it for the coalesce window.
func (w *wrapped) emit(ev Event) {
	if w.opts.CoalesceMS <= 0 {
		w.send(ev)
		return
	}
	w.coalMu.Lock()
	w.coalBatch = append(w.coalBatch, ev)
	if w.coalTimer == nil {
		w.coalTimer = time.AfterFunc(
			time.Duration(w.opts.CoalesceMS)*time.Millisecond,
			func() {
				select {
				case w.flushCh <- struct{}{}:
				case <-w.done:
				}
			},
		)
	}
	w.coalMu.Unlock()
}

// flushCoalesceBatch runs in pump: drains the batch to `out` in
// arrival order. Callable from timer signal or shutdown path. Safe
// because pump is the only writer on `out`.
func (w *wrapped) flushCoalesceBatch() {
	w.coalMu.Lock()
	batch := w.coalBatch
	w.coalBatch = nil
	if w.coalTimer != nil {
		w.coalTimer.Stop()
		w.coalTimer = nil
	}
	w.coalMu.Unlock()
	for _, ev := range batch {
		w.send(ev)
	}
}

func (w *wrapped) send(ev Event) {
	select {
	case w.out <- ev:
	case <-w.done:
	}
}

func (w *wrapped) shouldSkipPath(rel string) bool {
	for _, part := range splitPath(rel) {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

func (w *wrapped) matches(rel string) bool {
	for _, pat := range w.opts.Exclude {
		if repo.DoublestarMatch(pat, rel) {
			return false
		}
	}
	if len(w.opts.Include) == 0 {
		return true
	}
	for _, pat := range w.opts.Include {
		if repo.DoublestarMatch(pat, rel) {
			return true
		}
	}
	return false
}

// splitPath breaks a forward-slash path into its segments.
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
