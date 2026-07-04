# WS01 — Reconcile & Git Hooks (freshness core)

Size: **M/L**. Depends on: nothing. Blocks: 02 (rescan target, `index_meta`),
03 (`last_full_scan_at`). Ships independently — part A alone already fixes
stale-deletes on every daemon restart and every commit.

## Problem

A git branch switch leaves the index wrong in both directions and nothing
ever repairs it:

- Only a `post-commit` hook exists (`internal/hook/install.go`); checkouts,
  merges, and rebases trigger no reindex. `.git` is in the watcher's
  `skipDirs` (`internal/watch/common.go:50`), so HEAD changes are invisible.
- The startup catch-up scan (`cmd/myco/cmd_daemon.go:88-95` →
  `pipeline.RunOnce`) only upserts files that exist. The only `DELETE FROM
  files` lives in `HandleChange`'s removed branch
  (`internal/pipeline/pipeline.go:344`). Files deleted or renamed while the
  daemon was down (or while events were lost) persist as ghost rows forever —
  which also poisons `search_lexical`, since it greps index-known paths and
  hits ENOENT (stderr-only, `internal/query/lexical.go:67-78`).

Key insight: once `RunOnce` becomes a true *reconcile* (upsert + prune),
every other freshness fix reduces to "find one more reliable trigger for
RunOnce". So the prune pass is the foundation; hooks are the most
user-visible trigger.

## A. Prune pass in `RunOnce`

Mechanism (`internal/pipeline/pipeline.go`):

1. After the symbol pass **and** the document pass complete, build the union
   set of walked rel-paths. `runDocuments` must contribute its walk results —
   either return its `[]repo.File` or record paths into a shared
   `map[string]struct{}`.
2. New writer method in `internal/index/index.go`:
   `PruneFilesExcept(ctx, keep map[string]struct{}) (pruned int, err error)` —
   `SELECT id, path FROM files`, diff in memory, chunked
   `DELETE FROM files WHERE id IN (...)` (~500 per statement) inside one
   transaction. FK cascades drop symbols/refs/documents/chunks.
3. Guard rails: skip the prune entirely when any walk returned an error or
   `ctx.Err() != nil`. A failed walk must never masquerade as "everything
   was deleted".
4. Prune keys on `path` alone: `files.path` is globally UNIQUE
   (`internal/index/migrations/0001_init.sql`), and in workspace mode the
   workspaces replace the walker entirely — anything not in the walked union
   is unreachable by definition (including legacy NULL-project rows after a
   workspace conversion; pruning those is correct).
5. `Report.FilesPruned int`; log it in the daemon catch-up line
   (`cmd/myco/cmd_daemon.go:93`).
6. Serialize concurrent reconciles: `sync.Mutex runMu` on `Pipeline`, held
   for the whole of `RunOnce`. `MethodReindex` can already race with itself
   today; part B makes concurrent triggers common (`git pull` fires
   post-checkout and post-merge back to back).

### Migration `0010_index_meta.sql` (additive)

```sql
CREATE TABLE index_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
```

`RunOnce` writes `last_full_scan_at` (unix seconds) at the end of a
successful reconcile. A new reader method in `internal/query` exposes it —
`MAX(last_indexed_at)` (`query.go:663-666`) is a write-time proxy that goes
stale on quiet repos even when scans run fine. WS02 (doctor) and WS03
(staleness hints) read it.

## B. post-checkout / post-merge / post-rewrite hooks

Mechanism:

1. **Generalize `internal/hook/install.go`**: replace
   `postCommitScript`/`InstallPostCommit`/`UninstallPostCommit` with a
   table-driven
   `ManagedHooks = []string{"post-commit", "post-checkout", "post-merge", "post-rewrite"}`
   plus `InstallAll(repoRoot) (installed []string, err error)` and
   `UninstallAll(repoRoot)`. The script template is the existing one with
   `myco hook %s` substituted; keep the `Managed by mycelium` marker and the
   `.mycelium-backup` semantics per hook file. `post-rewrite` covers
   `git rebase` / `commit --amend` — many files at once, zero extra cost.
2. **Generalize `internal/hook/run.go`**: one `RunHook(ctx, socketPath)` —
   all four hooks do the same thing: ping `MethodReindex`, no-op when the
   daemon is down. With part A the daemon-down no-op is fully safe: the
   startup catch-up now reconciles deletes too.
3. **CLI wiring** (`cmd/myco/cmd_misc.go:22-40`): add `post-checkout`,
   `post-merge`, `post-rewrite` subcommands under `myco hook` (loop over
   `ManagedHooks`). Git passes args (post-checkout gets `<old> <new> <flag>`)
   — accept and ignore via `cobra.ArbitraryArgs`.
4. **Wizard** (`cmd/myco/cmd_init.go:144-152`): swap `hook.InstallPostCommit`
   for `hook.InstallAll`; message names all four hooks.
5. **Uninstall** (`cmd/myco/cmd_misc.go:186-205`): loop `UninstallAll`;
   update prompt text.
6. Update daemon hint text and CHANGELOG.

## Risks

- File created between walk and prune gets deleted → the watcher event
  (debounce 200ms + coalesce 2s) re-adds it. Self-healing; document in a
  comment at the prune site.
- Oversize files (> `MaxFileSizeKB`) get pruned once they grow past the cap —
  that is a bug fix, not a regression (the watcher already drops their
  events, leaving stale rows today).
- `core.hooksPath` (husky etc.): `InstallAll` writes `.git/hooks`, which git
  then ignores. Detect via `git config core.hooksPath` in the wizard and
  warn ("hooks won't fire; the HEAD watcher / burst rescan still cover
  you"). WS02 is the actual coverage.
- Worktrees: `.git` is a file; `InstallPostCommit` already returns
  `(false, nil)` — preserve that behavior in `InstallAll`.
- Reindex storms on `git pull` (checkout + merge fire together): absorbed by
  `runMu` plus the content-hash short-circuit making the second pass cheap.

## Tests

- Integration (`test/integration/`): index a fixture → delete a file on
  disk → `RunOnce` → row and cascaded symbols gone, `FilesPruned == 1`.
- Workspace case: file removed from one project must not prune the other
  project's rows.
- Unit: chunked delete with > 500 stale rows.
- Integration test for the new `index_meta` reader (new-query-method rule).
- New `internal/hook/install_test.go`: temp `.git/hooks` dir — all four
  hooks written; backup/restore round-trip; foreign hook preserved on
  uninstall.
- `RunHook` against a stub unix-socket server asserting exactly one
  `MethodReindex` request (pattern: `internal/daemon/daemon_test.go`).
