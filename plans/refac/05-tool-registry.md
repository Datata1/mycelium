# 05 — Tool Registry

**Size:** M · **Depends on:** 03 (ipc DTOs), 04 (Service methods) ·
**Blocks:** nothing — this is the payoff workstream

## Goal

Collapse the six parallel tool-definition sites into **two sources of truth
plus one table**, with test-enforced parity. Adding a tool becomes: mcpschema
entry, ipc types, Service method, one registry line, one render func — and a
failing test enumerates whatever you forgot.

## Pain

Today the 12 tools are hand-maintained in six places that must stay in sync:

1. `pkg/mcpschema/tools.go` — JSON schemas + prose descriptions (public API).
2. `internal/ipc/proto.go` — method consts + typed `*Params` structs.
3. `internal/daemon/daemon.go:168 dispatchInner` — switch method → Reader call.
4. `internal/mcp/server.go:129-202 mapToolToIPC` — switch tool name → ipc
   method, re-unmarshalling every param type.
5. `internal/mcp/render/render.go:9 Render` — switch method → renderer.
6. `cmd/myco/cmd_query.go` — handler per tool (collapsed by WS04).

Drift is guaranteed — CLAUDE.md itself still lists a `get_definition` tool
that no longer exists anywhere in the code. (Fix CLAUDE.md's tool list as part
of this workstream.)

## Decision

**Hand-written table + small generic binder + parity tests.** Rejected:

- *Codegen from a single source* — a generator contributors must learn and
  run, build-step noise, heavy for 12 tools.
- *Schema derivation via reflection/struct tags* — the hand-written prose
  descriptions in mcpschema are the product (they prime agent tool choice)
  and no derivation produces them; `pkg/mcpschema` is public API whose shape
  must not churn.

Two irreducible sources remain: **`pkg/mcpschema`** (schemas + descriptions,
unchanged, API-stable) and **`internal/ipc`** (typed params/results/method
consts). The registry binds them and replaces sites 3–6.

## Design

```go
// internal/registry

type Handler func(ctx context.Context, svc *service.Service, raw json.RawMessage) (any, error)

type Tool struct {
    Method ipc.Method                    // == mcpschema tool name (invariant, test-enforced)
    Handle Handler
    Render func(json.RawMessage) string  // from internal/mcp/render
}

// bind is where the compiler checks each entry: P must match the service
// method's parameter type and R its result type.
func bind[P, R any](fn func(*service.Service, context.Context, P) (R, error)) Handler {
    return func(ctx context.Context, svc *service.Service, raw json.RawMessage) (any, error) {
        var p P
        if len(raw) > 0 {
            if err := json.Unmarshal(raw, &p); err != nil {
                return nil, fmt.Errorf("%w: %v", ipc.ErrBadParams, err)
            }
        }
        return fn(svc, ctx, p)
    }
}

var tools = []Tool{
    {ipc.MethodFindSymbol, bind((*service.Service).FindSymbol), render.FindSymbol},
    // ... 12 entries
}

func Lookup(m ipc.Method) (Tool, bool)
```

What each site becomes:

- **daemon** `dispatchInner` switch → `registry.Lookup(m)` + explicit
  special cases for `ping` and `reindex` (the write path stays visibly outside
  the read registry — sole-writer invariant).
- **mcp** `mapToolToIPC` → **deleted**. Tool name and ipc method are already
  identical strings 1:1 (verified against `pkg/mcpschema/tools.go` and
  `proto.go`). The MCP server forwards `name` + raw `arguments` over the
  socket; the daemon-side registry unmarshal is the real validation.
- **render** `Render` switch → the registry's `Render` field (the per-method
  render functions stay where they are; only dispatch moves).
- **CLI** — WS04's local-fallback `dispatch` uses `registry.Lookup` too, so
  daemon and fallback literally execute the same handler.

### Parity enforcement (`internal/registry/registry_test.go`)

- Add `ipc.AllMethods []Method` (read methods only) next to the consts.
- Assert set equality: `{mcpschema.Tools() names} == {registry methods} ==
  {ipc.AllMethods}`, and every entry has non-nil `Handle` and `Render`.
- Forgetting any site ⇒ `go test` fails with the missing name. This is the
  pragmatic "compile-time exhaustiveness" — true exhaustiveness over
  string-keyed sets isn't expressible in Go.

## Migration path (green at every step)

1. Add `internal/registry` + parity test while the old switches still exist
   (registry unused — green).
2. Point daemon dispatch at it; delete the switch.
3. Simplify the MCP server to pass-through; delete `mapToolToIPC`.
4. Move render dispatch.
5. Wire the CLI fallback (WS04's `dispatch`).
6. Docs: "adding a tool" checklist in CONTRIBUTING + fix the tool list in
   CLAUDE.md (drop `get_definition`).

## Risks

- MCP pass-through moves *where* bad-params errors surface (daemon instead of
  the MCP process) — error text seen by MCP clients may change slightly.
  Acceptable; note it in the CHANGELOG.
- Don't force `ping`/`reindex` into the registry for symmetry — they're
  different by design.

## Verification

- Parity test green; deliberately comment out one entry and confirm the test
  names it.
- Integration suite green.
- Manual MCP smoke: `tools/list` returns 12 tools; one `tools/call`
  (`find_symbol`) renders correctly through Claude Code or a raw JSON-RPC
  pipe.
