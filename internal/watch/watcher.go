package watch

import (
	"context"
	"log/slog"
)

// Event is a coalesced change notification for a single repo-relative
// path. The public `Watcher` guarantees bursts of fs events collapse
// into at most one Event per (debounce window + coalesce window).
//
// An Event with Overflow=true carries no path: it means the watcher can
// no longer account for every change (kernel queue overflow, a burst
// beyond RescanThreshold, a watchman fresh instance) and the consumer
// must reconcile the full tree. At most one Overflow event is emitted
// per coalesce window.
type Event struct {
	RelPath  string
	AbsPath  string
	Removed  bool // true if the file no longer exists
	Overflow bool // true if per-file accounting was lost — reconcile
	Reason   string
}

// Watcher is the backend-agnostic surface the daemon consumes.
// Implementations live in this package (fsnotify, watchman) and are
// constructed via New().
type Watcher interface {
	// Start registers watches and begins pumping events. Cancelling ctx
	// or calling Close stops the pump. Start returns after initial
	// registration completes (so callers can rely on the first-scan
	// ordering documented in the daemon).
	Start(ctx context.Context) error

	// Events returns the output channel. Closed after Start's pump
	// exits, whether via Close, context cancellation, or a terminal
	// backend error.
	Events() <-chan Event

	// Close tears down watches. Safe to call more than once.
	Close() error
}

// Backend selects the filesystem-event source.
type Backend string

const (
	BackendFsnotify Backend = "fsnotify" // default; pure Go, zero-install
	BackendWatchman Backend = "watchman" // opt-in; needs a running watchman
)

// Options carries the knobs every backend accepts. Moved from New()'s
// positional-arg list in v1.7 so adding another knob (like `Backend`)
// doesn't keep shifting positional call-sites around.
type Options struct {
	Root          string   // absolute repo root
	Include       []string // doublestar globs (nil = all)
	Exclude       []string // doublestar globs
	MaxFileSizeKB int      // drop events on files larger than this (0 = no limit)
	DebounceMS    int      // per-file quiet window before the event is emitted
	CoalesceMS    int      // cross-file batch window after debounce (0 = no batching)
	Backend       Backend  // BackendFsnotify (default) | BackendWatchman
	// RescanThreshold caps how many per-file events one coalesce window
	// may carry: past it the batch is replaced with a single Overflow
	// event (a git checkout touching hundreds of files is cheaper as one
	// reconcile than as per-file churn — and may have overflowed the
	// kernel queue anyway). 0 disables burst escalation.
	RescanThreshold int
	// Log receives backend errors that were previously swallowed. Nil is
	// safe (discard).
	Log *slog.Logger
}

// rawSource is the minimal internal surface each backend implements.
// The shared wrapper in common.go bolts debounce + coalesce + filters
// on top, so backends only deliver unfiltered path events.
//
// Backends emit one rawEvent per observed fs change. The wrapper is
// responsible for everything downstream of that.
type rawEvent struct {
	RelPath  string
	AbsPath  string
	Removed  bool
	Overflow bool // backend lost events — see Event.Overflow
	Reason   string
}

type rawSource interface {
	// start registers watches with the OS/daemon and begins pumping
	// raw events. It is not expected to be long-lived beyond the ctx.
	start(ctx context.Context) error
	// events returns the raw event channel. Closed on shutdown.
	events() <-chan rawEvent
	// close tears down the source. Idempotent.
	close() error
}
