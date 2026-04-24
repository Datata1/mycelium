package watch

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/jdwiederstein/mycelium/internal/watch/watchman"
)

// watchmanSource is a rawSource backed by a watchman subscription.
// It translates FileChange batches into rawEvents one-by-one and
// leaves all policy (debounce, coalesce, filters, size cap) to the
// shared wrapper — identical behavior to fsnotifySource.
type watchmanSource struct {
	root string
	sub  *watchman.Subscription

	out       chan rawEvent
	done      chan struct{}
	closeOnce sync.Once
}

// newWatchmanWatcher builds a fully-wrapped Watcher backed by watchman.
// It probes the socket eagerly so the caller in backend.go can fall
// back to fsnotify at construction time if watchman is unavailable.
// The real subscription is opened later in start() with the daemon's
// long-lived ctx.
func newWatchmanWatcher(opts Options) (Watcher, error) {
	src, err := newWatchmanSource(opts)
	if err != nil {
		return nil, err
	}
	return newWrapped(opts, src), nil
}

func newWatchmanSource(opts Options) (*watchmanSource, error) {
	absRoot, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("watchman: resolve root: %w", err)
	}
	// Eager socket probe: if the user picked backend=watchman and
	// watchman isn't installed, fail here so backend.New can fall
	// back to fsnotify with a clear warning.
	probeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := watchman.GetSocketPath(probeCtx); err != nil {
		return nil, fmt.Errorf("watchman: %w", err)
	}
	return &watchmanSource{
		root: absRoot,
		out:  make(chan rawEvent, 256),
		done: make(chan struct{}),
	}, nil
}

func (s *watchmanSource) events() <-chan rawEvent { return s.out }

func (s *watchmanSource) start(ctx context.Context) error {
	sock, err := watchman.GetSocketPath(ctx)
	if err != nil {
		return fmt.Errorf("watchman: %w", err)
	}
	sub, err := watchman.Subscribe(ctx, sock, s.root, "mycelium")
	if err != nil {
		return fmt.Errorf("watchman subscribe: %w", err)
	}
	s.sub = sub
	go s.pump(ctx)
	return nil
}

func (s *watchmanSource) close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		if s.sub != nil {
			err = s.sub.Close()
		}
	})
	return err
}

// pump drains subscription batches into individual rawEvents. Watchman
// reports exists=false when a file is deleted; that maps to our Removed
// flag. The abs path is reconstructed from the watched root so size-cap
// filtering in the wrapper can stat the file.
func (s *watchmanSource) pump(ctx context.Context) {
	defer close(s.out)
	in := s.sub.Updates()
	errs := s.sub.Errors()
	for {
		select {
		case <-ctx.Done():
			_ = s.close()
			return
		case <-s.done:
			return
		case batch, ok := <-in:
			if !ok {
				return
			}
			for _, f := range batch {
				rel := f.Name
				abs := filepath.Join(s.root, filepath.FromSlash(rel))
				select {
				case s.out <- rawEvent{RelPath: rel, AbsPath: abs, Removed: !f.Exists}:
				case <-s.done:
					return
				}
			}
		case <-errs:
			// Read-pump died — connection is gone. Stop emitting; the
			// wrapper will see the closed channel and shut down.
			_ = s.close()
			return
		}
	}
}
