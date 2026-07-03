# 06 — Language Unification

**Size:** S/M · **Depends on:** nothing (ideally lands before 04 so service
wiring uses it from day one) · **Blocks:** nothing

## Goal

No language is special: the Go resolver is injected exactly like TS/Python,
parser registration lives in one function, and `docs/adding-a-language.md`
describes code that actually exists.

## Pain

- `internal/pipeline/pipeline.go` imports `resolver/golang` concretely and
  keeps a legacy field `GoResolver *goresolver.Resolver` (`pipeline.go:68`)
  plus a fallback branch (`pipeline.go:475-481`) — even though the generic
  `Resolver` interface (`pipeline.go:24`) exists and `cmd/myco/shared.go:102
  loadResolvers` already injects all three resolvers (including Go, line 107)
  through the map. The legacy path is dead weight kept "for pre-v1.3
  construction".
- Deprecated `Pipeline.Walker` (`pipeline.go:51`) keeps a second legacy path
  alive (`pipeline.go:130-137, 284-285`).
- Parser registration is copy-pasted inline: `cmd/myco/cmd_index.go:34-42`
  and `cmd/myco/cmd_daemon.go:73-81`.
- `docs/adding-a-language.md` tells contributors to register in a
  `buildRegistry` function in `shared.go` — **that function does not exist**.
  The exact doc a new contributor follows is wrong.

## Constraints

Go parser stays stdlib `go/ast` — only *wiring* moves; no cgo for Go.
Parsers keep emitting plain structs and stay storage-ignorant.

## Design

New package `internal/languages` — the single place that knows which languages
exist (not `cmd/myco`: it must be importable by daemon wiring, WS04's service,
and tests):

```go
// Registry returns a parser registry with all built-in languages enabled
// per config (nil/empty = all).
func Registry(enabled []string) *parser.Registry

// Resolvers returns one ref resolver per enabled language, keyed by
// language name as pipeline expects.
func Resolvers(root string, enabled []string) map[string]pipeline.Resolver
```

- Bodies move verbatim from the two inline registration blocks and from
  `shared.go:loadResolvers`. Both cmd call sites (and later the service
  wiring) call these.
- Pipeline cleanup: delete `Pipeline.GoResolver` and the `resolverFor`
  legacy branch. **Implementation note (2026-07-03):** `Pipeline.Walker`
  stays — it is not legacy but the live single-root path
  (`buildWorkspaces` returns nil when no `projects:` are configured) and
  is constructed at 11 sites incl. all integration tests. Only its
  misleading "legacy" comment was corrected. Collapsing Walker into a
  synthesized root Workspace remains a possible follow-up in WS04.
- If WS02's resolver-ctx threading hasn't landed yet, do it here in the same
  PR that touches the `Resolver` interface.
- **Rewrite `docs/adding-a-language.md` against reality:** point at
  `internal/languages`, walk the `parser.Parser` interface
  (`internal/parser/types.go:84`), optional `pipeline.Resolver`, optional
  `InheritanceEmitter`, integration fixture expectations
  (`test/integration/testdata/`), config defaults. Link the parser
  table-driven tests (WS07-A) as executable examples.

## Migration path (green at every step)

1. Add `internal/languages` delegating to the existing inline code; switch
   both cmd call sites — behavior identical.
2. Route the Go resolver exclusively through the map (it already is via
   `loadResolvers`; confirm nothing else sets `GoResolver`).
3. Delete the deprecated fields + legacy branches in one PR.
4. Docs rewrite.

## Risks

Low. The only trap is a test or `internal/bench` constructing `Pipeline` via
the deprecated fields — the pre-deletion grep catches it.

## Verification

- Integration suite green (index/interface suites exercise all three
  languages end-to-end).
- Pipeline benchmarks unchanged.
- Doc verified by literally following it to stub a toy language (e.g. a
  10-line "lua" parser) in a scratch branch — do not commit the toy.
