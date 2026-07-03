# 02 — Error & Logging Foundations

**Size:** M · **Depends on:** nothing · **Blocks:** 03, 04, 05 (they build on
these semantics — doing this later means touching the same lines twice)

## Goal

Real failure semantics (sentinel errors that survive the wire) and structured
logging, so the dispatch/service refactors and the test buildout have solid
ground to stand on.

## Pain

- **Zero sentinel errors in the module.** "not found" and "unknown method" are
  string-formatted and can only be string-matched by callers across the
  transport boundary.
- `errSkipped` (`internal/pipeline/pipeline.go:354`) is compared with `==`
  (`pipeline.go:218`) — breaks the moment anyone wraps it.
- Two byte-identical `Logger` interfaces: `internal/daemon/daemon.go:37` and
  `internal/pipeline/pipeline.go:76`. No levels, no structure.
- Response-write errors silently swallowed:
  `_ = json.NewEncoder(conn).Encode(&resp)` at `daemon.go:309,314`.
- Resolvers mint `context.Background()` internally
  (`internal/resolver/python/resolver.go:51`,
  `internal/resolver/typescript/resolver.go:53`); CLI query handlers ignore the
  signal-rooted command context and mint their own `context.Background()`
  (`cmd/myco/cmd_query.go`, throughout).
- Stringly-typed protocol: ipc methods are bare `string` consts
  (`internal/ipc/proto.go:18-31`); watcher backend compared against literals
  `"fsnotify"`/`"watchman"`; `query.Direction` enum exists but the wire layer
  passes raw strings (`daemon.go:242`-ish).
- `query.ReadFocusedPreviewLines` (`internal/query/read.go:42`) is an exported
  **mutable package var** used as config.

## Design

### Sentinels + wire error codes

Producer-side sentinels, matched with `errors.Is`:

```go
// internal/query/errors.go
var ErrNotFound = errors.New("not found")
// usage: fmt.Errorf("symbol %q: %w", name, ErrNotFound)

// internal/ipc/proto.go
var (
    ErrUnknownMethod = errors.New("unknown method")
    ErrBadParams     = errors.New("bad params")
)
```

`ipc.Response` gains an **additive** field — old clients ignore it, JSON stays
backward compatible:

```go
type Response struct {
    OK     bool            `json:"ok"`
    Result json.RawMessage `json:"result,omitempty"`
    Error  string          `json:"error,omitempty"`
    Code   string          `json:"code,omitempty"` // "not_found" | "unknown_method" | "bad_params"
}
```

- Daemon maps sentinel → code via `errors.Is` when building the response.
- `ipc.Client` maps code → sentinel when decoding, wrapping so the original
  message is preserved: callers write `errors.Is(err, query.ErrNotFound)`
  instead of `strings.Contains(err.Error(), ...)`.
- This is what makes CLI exit codes and MCP error rendering principled later.
- `errSkipped`: keep unexported, switch the comparison to `errors.Is`.

### slog

- Both `Logger` interfaces are deleted; `Daemon` and `Pipeline` get a
  `Log *slog.Logger` field. Nil-safety via default
  `slog.New(slog.DiscardHandler)` (requires Go 1.24+ — coordinate with the
  WS01 go-directive decision; fallback: a discard handler helper).
- Daemon wires `slog.NewTextHandler(os.Stderr, nil)` with a `component=daemon`
  attr; pipeline logs through the same logger passed down from the daemon/CLI.
- `daemon.go:309,314`: log encode failures at `Warn` with the method name.
- Cheap follow-on (not required): `--log-format=json` flag on `myco daemon`.

### Context threading

- `pipeline.Resolver` interface gains `ctx` as first parameter of its resolve
  method; all three implementations and both call sites updated in one PR
  (internal interface — free to change).
- CLI query handlers use `cmd.Context()` (cobra) instead of
  `context.Background()`, so Ctrl-C cancels in-flight queries.

### Typed strings (small wins, bundled)

- `type Method string` in ipc; the consts become typed; `Request.Method Method`
  — JSON encoding unchanged (underlying string).
- `type Backend string` in `internal/watch` with `BackendFsnotify`,
  `BackendWatchman`; config validation compares consts.
- Daemon parses wire direction string → `query.Direction` at the boundary and
  passes the typed value inward.
- `query.ReadFocusedPreviewLines` → unexported field on `Reader` with a
  default of 50 and a setter (or option) for the one place that tunes it.

## Migration path (each bullet an independent, small PR)

1. Sentinels + `Code` field + client mapping, with an integration test
   asserting a not-found round-trips through the socket to `errors.Is`.
2. slog swap (mechanical; delete both interfaces).
3. Context threading (resolver signature + CLI handlers).
4. Typed strings.
5. Package-var removal.

Wire JSON changes only by the additive `code` field.

## Risks

- Anything string-matching today's error text (hook scripts, telemetry
  parsing, tests) breaks silently — grep for `strings.Contains` on error
  values and for daemon-log scraping before merging.
- slog changes daemon log line format; `myco doctor` / session tooling must
  not parse old-format lines (verify by grep).

## Verification

- Integration suite green; new unit test: error-code round-trip through
  `ipc.Client` for all three codes.
- `errorlint` (WS01) enforces `errors.Is` usage from here on.
- Manual: `myco find doesnotexist` exits non-zero with a clean message, daemon
  up and daemon down.
