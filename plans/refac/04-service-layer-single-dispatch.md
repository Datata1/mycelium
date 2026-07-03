# 04 — Service Layer & Single Dispatch

**Size:** M/L · **Depends on:** 02, 03 step 2 (DTOs in ipc) · **Blocks:** 05,
07-C

## Goal

One place where "method + params → result" executes, shared by daemon and CLI.
The 13 duplicated dual-path handlers in `cmd/myco/cmd_query.go` collapse to one
generic dispatch function; the `*sql.DB` leak through `index.Index.DB()`
disappears.

## Pain

- `cmd_query.go` (618 LOC) implements every tool **twice**: daemon socket if
  reachable, else `query.NewReader(ix.DB())` directly — 9 `NewReader` sites in
  cmd_query.go alone, 13 module-wide across cmd_daemon/init/stats/doctor/misc.
- `index.Index.DB()` hands the raw `*sql.DB` to `package main`.
- Daemon dispatch (`internal/daemon/daemon.go:168 dispatchInner`) is
  untestable without a real socket + DB.
- `--since` resolution happens daemon-side (`daemon.go:288 resolveSince`) —
  the CLI fallback has its own copy, so semantics can drift.
- `cmd/myco` (3386 LOC) carries business wiring (workspaces, resolvers,
  dual-path logic) in `package main`.

## Decision (owner-confirmed)

**Shared service layer** — the CLI keeps working without a daemon. Rejected:
daemon-autostart (UX change: background process the user didn't start) and
codegen of both sides (tooling overhead for ~13 tools).

## Design

New package `internal/service`:

```go
// Service owns read-path execution. It is the only component that
// constructs a query.Reader; nothing outside internal/service (and the
// pipeline write path) touches *sql.DB.
type Service struct {
    reader *query.Reader
    root   string        // repo root, for --since resolution
    log    *slog.Logger
}

func NewReadOnly(ix *index.Index, root string, log *slog.Logger) *Service

// One typed method per tool, in wire types (ipc params in, ipc DTOs out).
func (s *Service) FindSymbol(ctx context.Context, p ipc.FindSymbolParams) (ipc.FindSymbolResult, error)
func (s *Service) GetReferences(ctx context.Context, p ipc.GetReferencesParams) (...)
// ... one per ipc read method (12 total)
```

- **Read-only by construction**: no pipeline handle — the sole-writer
  invariant holds structurally, not by convention. `reindex` stays a
  daemon-only socket method; `myco index` keeps its direct pipeline wiring.
- `--since` resolution (`gitref.ResolveSince`) moves **into** Service methods
  that accept `Since`, so daemon and CLI fallback get identical semantics.
  `daemon.resolveSince` is deleted.
- Daemon: each `dispatchInner` case body becomes a one-line Service call
  (WS05 then deletes the switch entirely).
- CLI: `daemonClient(rc)` probe stays. Each `runQueryX` becomes
  *build params → `dispatch(ctx, rc, method, params)`* where `dispatch` tries
  the socket and falls back to a lazily constructed local `Service` — **one**
  function, not 13 handler pairs.
- After the last leak site is converted, narrow `index.Index.DB()`:
  unexport it and give pipeline/service an internal accessor, or move the
  Reader construction into `index` — pick whichever keeps the
  query-is-sole-reader invariant most legible. Remaining `DB()` users after
  the grep must be pipeline and service only.
- cmd/myco slimming rides along: `buildWorkspaces` / `loadResolvers` move out
  of `package main` (`cmd/myco/shared.go:34,102`) into `internal/service` and
  `internal/languages` (WS06); cmd files become flag parsing + call + render.

## Migration path (green at every step)

1. Introduce `internal/service` wrapping Reader; daemon cases delegate —
   behavior-identical, integration suite green.
2. Move `--since` resolution into Service (delete both copies).
3. Convert `cmd_query.go` handlers one at a time to the shared `dispatch`
   helper.
4. Convert the remaining `NewReader(ix.DB())` sites (doctor, stats, init,
   misc) to Service.
5. Narrow `Index.DB()`.

## Risks

- Latent daemon-vs-fallback divergence today (e.g. Since handling) gets
  *unified* — which can surface as a behavior change. Diff both paths' output
  before/after and document any intentional fix.
- CLI latency: keep the existing cheap socket-reachability probe; don't add
  retries.
- `doctor`/`stats` may need read methods that aren't ipc tools — Service can
  expose extra non-wire methods; that's fine, it's internal.

## Verification

- Integration suite green after each step.
- **New permanent guard:** a suite running every query subcommand twice —
  daemon up and daemon down — asserting identical stdout (this is the
  dual-path equivalence test, kept forever).
- Unit tests of Service against a fixture DB (WS07-B harness).
- `grep -rn "\.DB()" --include="*.go"` shows only pipeline/service/index
  internals.
