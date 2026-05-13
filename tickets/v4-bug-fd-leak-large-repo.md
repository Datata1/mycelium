# BUG (P0) — Daemon hits `EMFILE` (`too many open files`) on large repos

**Priority:** P0 for v4 — read_focused fails on monorepo-scale repos
**Surfaced by:** `tickets/v4-f1-findings.md` (T2)
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Blocks:** any TS / Python field test on a real workspace repo;
v4.0 release.

## Problem

In Codesphere `monorepo-4` (3079 indexed files across 49
workspace projects), the agent called `read_focused` and got:

> `daemon: read /Users/.../plans.ts: open ...: too many open files`

This is a hard `EMFILE` from the OS — the daemon process has hit
its `RLIMIT_NOFILE` cap and `os.ReadFile` can't open another
descriptor. After this, every subsequent file-touching myco call
on this daemon will fail until file descriptors are freed.

The user's workaround was to fall through to the agent's general-
purpose `Read` (which runs in the agent's own process, not the
daemon's). v4 B1 made `read_focused` honest about no-focus calls,
which only matters if `read_focused` *can* read the file at all.

## What's wrong (hypothesis)

mycelium's daemon is a long-running process that:

1. Holds the SQLite WAL connection (3-4 fds).
2. Holds an fsnotify watcher per *directory* the include glob
   covers (typical Linux fsnotify backend = inotify, 1 fd per
   watched directory).
3. Opens files transiently for `read_focused` /
   `get_file_outline` / etc. (closes them after read).
4. The HTTP and unix-socket listeners (a couple of fds).

(2) is the suspect. monorepo-4 has 49 workspace projects, with
typical 5-50 subdirectories each → 500-2000 watched directories
→ 500-2000 fds just for the watcher. macOS default `RLIMIT_NOFILE`
is 256 (very low); Linux user default is 1024.

The v3.x LIMITATIONS.md row already names this:

> fsnotify hits inotify limits on 100k+ file repos (default
> `fs.inotify.max_user_watches = 8192`) | Linux kernel cap |
> Planned **v1.7** Watchman opt-in backend

v1.7's Watchman backend was tagged but never actually shipped (per
the v3.1 plan's "Watchman backend (was tagged v1.7 but never
shipped — folded into v3.2 unless a user actually hits the inotify
cap)"). F1 just hit the cap. v4 has to ship the fix.

## Verification (do this before coding)

Reproduce on the affected daemon (run on the user's machine, in
the affected repo, while the daemon is up):

```bash
# (a) Daemon process fd count vs limit
PID=$(pgrep -x myco)
echo "fd count: $(ls /proc/$PID/fd 2>/dev/null | wc -l)"   # Linux
# or: lsof -p $PID | wc -l   # macOS / Linux fallback
echo "soft limit: $(prlimit --pid=$PID --nofile=:: 2>/dev/null | tail -1)"
# or: launchctl limit maxfiles  # macOS system-wide

# (b) How many directories does the include glob cover?
find . -path ./.git -prune -o -path ./node_modules -prune -o -type d -print | wc -l

# (c) doctor's existing inotify check, if it surfaced anything
myco doctor --json | jq '.checks[] | select(.name == "inotify_headroom")'
```

If (a) shows fd count near the soft limit AND (b) is in the
hundreds-to-thousands range → fsnotify watcher exhaustion
confirmed.

The doctor's inotify_headroom check at
`internal/doctor/checks.go` (Linux-only) checks
`max_user_watches`, but **doesn't check `RLIMIT_NOFILE`**. That's
the missing diagnostic.

## What changes

The fix has three layers, ship in this order:

### 1. Diagnostic surface (smallest, ship today)

- `internal/doctor/inotify_*.go` (Linux + other): add a
  **process-fd-vs-rlimit** check. Open `/proc/self/fd` (Linux),
  count entries, compare to `getrlimit(RLIMIT_NOFILE)`. WARN above
  60% utilisation, FAIL above 90%.
- macOS path uses `getrlimit` directly (no `/proc/self/fd`).
- The check exposes a clear "you're about to crash" signal so
  users see this before hitting it in the field.

### 2. Real fix: ship Watchman backend (deferred since v1.7)

- The pluggable watcher interface already exists per CHANGELOG
  (v1.7 entry: *"Pluggable `internal/watch.Watcher` interface;
  `watcher.backend: watchman` in config; inotify headroom check"*).
- Whatever didn't ship needs to ship now. `internal/watch/watchman/`
  exists per the package list — finish wiring or fix what broke.
- `myco doctor` should switch its hint from
  "switch to watchman or raise the limit when this climbs" to a
  concrete `watcher.backend: watchman` config snippet ready to
  paste.
- `myco init` wizard: when the system has watchman installed AND
  the repo is over a size threshold (say 5000 files), default
  `watcher.backend: watchman` instead of fsnotify.

### 3. Belt-and-braces: raise our own ulimit

- On daemon startup, `setrlimit(RLIMIT_NOFILE, hard, hard)` to
  raise the soft limit to the hard limit. Linux/macOS both
  permit this without root. Doesn't fix the root cause but buys
  meaningful headroom on macOS where soft default is 256 and hard
  is 10240.
- Log the new limit at daemon startup so users see the bump.
- Document in `docs/` that for >50k-file repos they may need to
  bump the system hard limit too.

## Critical files

- `internal/doctor/checks.go` (or wherever the existing inotify
  check lives) — new fd-vs-rlimit check.
- `internal/watch/watchman/` — finish the v1.7 work.
- `internal/watch/` — the watcher interface; verify the
  Watchman backend implements it.
- `cmd/myco/main.go` `daemon` subcommand — add the
  setrlimit-on-startup call + log.
- `internal/wizard/` — auto-pick watchman backend during
  `myco init` when applicable.
- `internal/config/config.go` — `WatcherConfig.Backend` already
  exists; verify it's plumbed through.
- `docs/` — add a troubleshooting page for the EMFILE case.

## Acceptance criteria

- `task check` passes.
- New doctor check `daemon_fd_headroom` (or similar name): tests
  for the WARN and FAIL boundaries via `setrlimit` in test setup.
- On `monorepo-4` (the F1 reference repo): daemon starts with
  `watcher.backend: watchman`, doctor's new fd check reports OK,
  `read_focused` succeeds on every indexed file across 5+ test
  reads.
- The fall-through path documented: when watchman isn't installed
  and fsnotify hits the limit, daemon should log a clear "switch
  to watchman" message and continue to **fail gracefully** (return
  an error from the failing tool call) instead of crashing.
- LIMITATIONS.md updated: the inotify row moves from "Planned
  v1.7 Watchman backend" to "Watchman backend shipped in v4 —
  enable via `watcher.backend: watchman`".

## What this enables

- **Large-repo TS / Python adoption.** monorepo-4 scale (3000+
  files, 49 packages) is the *small* end of monorepo deployments
  myco needs to handle for v4 to claim agent-native readiness.
- **F1 + F2 field tests can complete.** Both the planned
  Python/Django and Rust/Axum tests target real codebases that
  will likely hit this same wall.
- **The doctor adoption story closes one degree more.** read_focused-
  under-used WARN currently can't distinguish "agent didn't reach
  for the tool" from "agent reached but the tool failed under fd
  pressure". After this fix, the WARN is true under-use signal.

## Out of scope

- **Cross-process file-handle pooling** (i.e. cache file handles
  across reads to amortise open/close). Premature; fixing the
  watcher leak is the load-bearing change.
- **Auto-installing watchman** when not present. Detect and
  recommend; don't install. Watchman has its own deployment story.
- **Switching the default backend to watchman repo-wide.** Default
  stays fsnotify (zero-install). Wizard recommends watchman when
  appropriate; users can also set globally.
- **Windows / WSL fd handling.** Different kernel layer; out of
  scope for v4 — still Linux + macOS first.

## Honest caveats

- Watchman is a separate binary that needs to be installed
  (homebrew, apt, etc.). The wizard can detect-and-recommend but
  not auto-install. Users without watchman + with large repos
  remain at the inotify cap until they install — document this
  prominently.
- The `setrlimit` belt-and-braces fix is a soft mitigation; on
  systems with low hard limits (some container environments,
  CI runners), it can't help.
- The doctor fd-headroom check relies on `/proc/self/fd` on
  Linux. Some hardened containers mask `/proc`; the check should
  degrade gracefully (skip, not error) in that environment.
- The `inotify_headroom` doctor check from v1.7 already exists
  and is conceptually similar; the new check is **process-side**
  fd headroom (different signal — covers macOS where the Linux
  inotify check is a no-op).
