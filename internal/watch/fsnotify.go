package watch

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// fsnotifySource is the default rawSource: a plain fsnotify watcher
// that walks the repo once at startup, registers directories, and
// auto-registers new directories as they're created.
//
// It produces unfiltered rawEvents; all policy (debounce, coalesce,
// includes/excludes, size cap) lives in the shared wrapper.
type fsnotifySource struct {
	root string
	fsw  *fsnotify.Watcher

	out       chan rawEvent
	done      chan struct{}
	closeOnce sync.Once
}

func newFSNotifySource(opts Options) (*fsnotifySource, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("new fsnotify watcher: %w", err)
	}
	return &fsnotifySource{
		root: opts.Root,
		fsw:  fsw,
		out:  make(chan rawEvent, 256),
		done: make(chan struct{}),
	}, nil
}

func (s *fsnotifySource) events() <-chan rawEvent { return s.out }

func (s *fsnotifySource) start(ctx context.Context) error {
	if err := s.addTree(s.root); err != nil {
		return fmt.Errorf("register watches: %w", err)
	}
	go s.pump(ctx)
	return nil
}

func (s *fsnotifySource) close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.fsw.Close()
	})
	return err
}

// addTree walks the repo once and registers every non-skipped
// directory with fsnotify. Failures on individual directories are
// swallowed — losing a few watches is better than refusing to start.
func (s *fsnotifySource) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] && path != root {
				return filepath.SkipDir
			}
			_ = s.fsw.Add(path)
		}
		return nil
	})
}

// pump translates fsnotify events into rawEvents. Directory creates
// trigger a recursive register so nested files emit subsequent events.
func (s *fsnotifySource) pump(ctx context.Context) {
	defer close(s.out)
	for {
		select {
		case <-ctx.Done():
			_ = s.close()
			return
		case <-s.done:
			return
		case ev, ok := <-s.fsw.Events:
			if !ok {
				return
			}
			s.handle(ev)
		case _, ok := <-s.fsw.Errors:
			if !ok {
				return
			}
			// fsnotify errors are non-fatal; the daemon surfaces
			// inotify-limit pressure via `myco doctor` instead.
		}
	}
}

func (s *fsnotifySource) handle(ev fsnotify.Event) {
	if ev.Has(fsnotify.Create) {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			name := filepath.Base(ev.Name)
			if !skipDirs[name] {
				_ = s.fsw.Add(ev.Name)
			}
			return
		}
	}
	rel, err := filepath.Rel(s.root, ev.Name)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	removed := ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)
	select {
	case s.out <- rawEvent{RelPath: rel, AbsPath: ev.Name, Removed: removed}:
	case <-s.done:
	}
}
