package watch

import (
	"fmt"
	"os"
)

// New constructs a public Watcher from the given Options. It picks the
// backend based on opts.Backend:
//
//   - "" or "fsnotify" — default, pure-Go, zero-install.
//   - "watchman"       — opt-in; talks to a locally-running watchman
//     daemon via unix socket. If the binary is missing or the socket
//     can't be located, we log a warning to stderr and fall back to
//     fsnotify so the daemon still starts.
//
// Unknown backend values are a hard error (typos shouldn't silently
// land on fsnotify).
func New(opts Options) (Watcher, error) {
	switch opts.Backend {
	case "", "fsnotify":
		fmt.Fprintln(os.Stderr, "[watch] backend=fsnotify")
		return newFSNotifyWatcher(opts)
	case "watchman":
		w, err := newWatchmanWatcher(opts)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"[watch] watchman unavailable: %v — falling back to fsnotify\n",
				err)
			return newFSNotifyWatcher(opts)
		}
		fmt.Fprintln(os.Stderr, "[watch] backend=watchman")
		return w, nil
	default:
		return nil, fmt.Errorf("unknown watcher backend %q (fsnotify | watchman)", opts.Backend)
	}
}

// newFSNotifyWatcher wraps an fsnotifySource with the shared common
// wrapper. Split out so the factory can call it from both the default
// path and the watchman-fallback path.
func newFSNotifyWatcher(opts Options) (Watcher, error) {
	src, err := newFSNotifySource(opts)
	if err != nil {
		return nil, err
	}
	return newWrapped(opts, src), nil
}
