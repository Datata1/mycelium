// Package watchman is a minimal in-tree client for Facebook's
// watchman file-watching service. It speaks JSON over a unix socket —
// the simplest wire mode watchman supports — and implements just
// enough of the protocol for mycelium's event stream:
//
//   - locate the socket via `watchman get-sockname --no-pretty`
//   - watch-project the mycelium repo root
//   - subscribe to "file of type f" events
//   - stream them as FileChange values over a Go channel
//
// It does NOT implement BSER, triggers, clocks-as-sync-primitive,
// glob expression pushdown, or auto-reconnect. v1.7 treats a dropped
// connection as a fatal error surfaced to the daemon — mirroring how
// the fsnotify backend treats its own closure.
//
// Protocol reference: https://facebook.github.io/watchman/docs/cmd/subscribe.html
package watchman
