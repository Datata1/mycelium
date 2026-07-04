# WS02 — Watcher Hardening (overflow → rescan, HEAD watch, doctor freshness)

Size: **M**. Depends on: 01 (RunOnce must prune for a rescan to heal;
`index_meta` feeds the doctor check). Ships independently of everything
else. Covers the flows hooks can't reach: `core.hooksPath` repos (husky),
IDE-driven git, `git stash pop` storms, and OS-level event loss.

## Problem

- A checkout rewrites hundreds of files at once — the classic inotify queue
  overflow / kqueue FD exhaustion case. fsnotify errors are silently
  swallowed (`internal/watch/fsnotify.go:95-101`); lost events are never
  recovered while the daemon runs. There is no periodic reconciliation —
  catch-up runs only at process start.
- Even without overflow, a burst of N hundred per-file `HandleChange` calls
  is strictly worse than one `RunOnce` (which short-circuits on content
  hash and now prunes).
- `myco doctor` cannot see any of this: it never re-walks by design
  (`internal/doctor/doctor.go:5-7`), and the only related check
  (`inotify_headroom`) is Linux-only.

## A. Overflow escalation

1. **Event types** (`internal/watch/watcher.go`): add `Overflow bool` and
   `Reason string` to both `Event` and `rawEvent`. An Overflow event means
   "state unknown — reconcile".
2. **fsnotify errors** (`fsnotify.go:95-101`): stop swallowing. Add an
   `Options.Log *slog.Logger` field (the watch package currently cannot
   log); log every error. When `errors.Is(err, fsnotify.ErrEventOverflow)`
   (check the fsnotify version in go.mod; if too old, treat *any* watcher
   error as overflow), emit
   `rawEvent{Overflow: true, Reason: "fsnotify: " + err.Error()}`.
3. **Burst detection** (`common.go`): in `wrapped.emit`, when a coalesce
   batch crosses `Options.RescanThreshold` (new config key
   `watcher.rescan_threshold`, default ~400; wire through
   `internal/config/config.go` `WatcherConfig` and the `watch.Options`
   literal in `cmd_daemon.go`), drop the batch and replace it with a single
   `Event{Overflow: true, Reason: "burst"}`. In `handle`, Overflow raw
   events bypass filters/debounce and go straight to the emit channel;
   dedupe to at most one pending Overflow per coalesce window.
4. **Watchman** (`watchman.go`, `watchman/subscription.go:152`): plumb
   `IsFreshInstance` out (currently read then ignored) and map it to one
   Overflow event — a fresh instance means deletions during the gap are
   unknown, which per-file events can't express. Skip the per-file dump for
   that delivery.
5. **Daemon** (`internal/daemon/daemon.go:84-93`): in the event pump,
   `if ev.Overflow` → non-blocking send on a new `rescanCh` (buffered,
   cap 1, so storms collapse into one pending rescan) instead of
   `HandleChange`. A dedicated goroutine drains `rescanCh` and calls
   `Pipeline.RunOnce(ctx)`, logging reason and report.

## B. `.git/HEAD` watch (zero-config checkout detection)

`skipDirs` only affects the repo-tree watcher; a **separate, non-recursive**
fsnotify watch on `<root>/.git` (an `Add` on a dir reports direct children
only) sees HEAD rewrites. Git updates `.git/HEAD` atomically via rename on
every branch switch, so a Create/Rename/Write event with basename `HEAD` is
a reliable checkout signal. Merges/commits do *not* rewrite HEAD (they
update `refs/heads/*`) — fine: worktree writes cover those, and part A
covers merge bursts.

- New `internal/watch/githead.go`:
  `StartHEADWatch(ctx, repoRoot string, log *slog.Logger) (<-chan struct{}, error)`
  — resolve `.git` (if it is a *file*, parse the `gitdir:` line for
  worktrees), one fsnotify watcher on that dir, filter
  `filepath.Base(ev.Name) == "HEAD"`, debounce ~500ms, emit ticks. Noise
  from `.git/index`, `ORIG_HEAD` etc. is filtered by the basename check.
  Failure to start is non-fatal (log, return nil channel).
- `cmd/myco/cmd_daemon.go` + `internal/daemon/daemon.go`: fan the tick
  channel into the same `rescanCh` from part A.

## C. doctor freshness check

- New reader method `query.(*Reader).SampleFiles(ctx, n int)
  ([]FileFreshnessRow, error)` — `path, projectRoot, mtime_ns,
  last_indexed_at` via `ORDER BY RANDOM() LIMIT ?` with the projects LEFT
  JOIN pattern from `lexical.go`.
- New `internal/doctor/freshness.go`, default check `index_freshness`:
  sample ≤ 200 rows, `os.Stat` each (abs path = repoRoot/projectRoot/path),
  count `missing_on_disk` and `mtime_newer_than_indexed` (disk mtime >
  stored `mtime_ns`). Thresholds in `doctor.Thresholds`: warn at ≥ 2 stale
  or ≥ 1%, fail at ≥ 10%. Include `last_full_scan_at` from `index_meta` in
  the message ("last reconcile: 2m ago"). 200 stats are sub-millisecond —
  consistent with doctor's "deliberately cheap" contract; update the package
  doc comment (`doctor.go:5-7`) to say the freshness check stats a sample
  but never re-walks *by default*.
- `--deep` flag on `cmd/myco/cmd_doctor.go`: build walkers from `rc.Cfg`
  (extract a shared helper with `cmd_daemon.go:75-77` / `buildWorkspaces`)
  and set-diff walked paths vs `SELECT path FROM files` → exact
  `on_disk_not_indexed` / `indexed_not_on_disk` counts with up to 5 example
  paths. Pass walk results into `doctor.Run` via an optional param so
  doctor never depends on walker construction.

## Risks

- Threshold too low → checkouts always full-rescan. Acceptable: with
  content-hash short-circuits a mostly-no-op `RunOnce` is cheap (it already
  runs on every commit via the hook).
- Watchman fresh-instance on daemon start duplicates the startup catch-up —
  the cap-1 channel plus WS01's `runMu` make it a cheap second pass.
- One extra inotify watch for `.git` — negligible. Bare/odd layouts: fail
  open (no channel).

## Tests

- `internal/watch/watch_test.go`: fake `rawSource` emitting Overflow →
  wrapped emits exactly one Overflow Event; burst of RescanThreshold+1
  files → single Overflow, no per-file events; burst below threshold →
  per-file events unchanged.
- `internal/daemon/daemon_test.go`: fake Watcher emitting N overflow
  events → `RunOnce` invoked once.
- githead unit test: temp dir with fake `.git/HEAD`; rewrite via
  write-temp + rename (mimicking git) → one tick; write `.git/index` → no
  tick; worktree `gitdir:`-file resolution.
- Doctor integration: index fixture → touch one file + delete another →
  warn with correct counts; `--deep` on a fixture with an extra unindexed
  `.go` file reports it. `SampleFiles` gets an integration test
  (new-query-method rule).
