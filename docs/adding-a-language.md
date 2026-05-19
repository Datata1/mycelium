# Adding a language to Mycelium

Mycelium's parser layer is a small, stable interface. Adding a new language
requires no changes outside the files listed here.

---

## 1. Implement the Parser interface

The contract is in [internal/parser/types.go](../internal/parser/types.go):

```go
type Parser interface {
    Language() string              // "rust", "java", etc.
    Supports(path string) bool     // true for files this parser handles
    Parse(ctx context.Context, path string, content []byte) (ParseResult, error)
}
```

Rules:
- `Parse` must be safe to call concurrently.
- `ParseResult.Symbols` — one entry per top-level and nested definition.
  Fill `Symbol.ParentName` for nested items (methods, inner types).
- `ParseResult.References` — one entry per call / import / type-use site.
  Set `Reference.ResolverVersion = 0` for raw textual refs; only resolvers
  bump this above zero.
- `ParseResult.ContentHash` and `ParseResult.ParseHash` — SHA-256 of the
  raw source bytes and the parse output respectively. The pipeline uses
  these to skip writes when nothing changed.

Place the implementation in:

```
internal/parser/<lang>/parser.go
internal/parser/<lang>/doc.go       // package comment
```

Use tree-sitter for languages that have a grammar (`github.com/smacker/go-tree-sitter`).
Use stdlib AST packages where they exist (Go's `go/ast` is the canonical example — no cgo).

---

## 2. Register the parser

In [cmd/myco/shared.go](../cmd/myco/shared.go), `loadResolvers` already
builds the per-language resolver map. Register the new parser in `buildRegistry`
(called from daemon, index, and session commands) by adding:

```go
reg.Register(yourlang.New())
```

`internal/parser/registry.go` does first-match dispatch by `Supports`, so
registration order only matters when two parsers claim the same extension.

---

## 3. (Optional) Implement a Resolver

Raw textual refs (`ResolverVersion=0`) work for basic call graphs. For
precise cross-file resolution, implement `pipeline.Resolver`:

```go
type Resolver interface {
    ResolveFile(absPath string, pr *parser.ParseResult) (resolved, total int)
    Ready() bool
}
```

`ResolveFile` rewrites `pr.References` in place, setting `DstName` to the
qualified form and bumping `ResolverVersion` to ≥ 1 on each rewritten ref.
`Ready()` returns false when the resolver hasn't finished loading (e.g. while
`go/packages` type-checks).

Place the resolver in:

```
internal/resolver/<lang>/resolver.go
internal/resolver/<lang>/doc.go
```

Register it in `cmd/myco/shared.go:loadResolvers`:

```go
if slices.Contains(languages, "yourlang") {
    m["yourlang"] = yourlangresolver.New(repoRoot)
}
```

---

## 4. Add an integration test fixture

Add a minimal source file to:

```
test/integration/testdata/fixtures/sample/<lang>/
```

Then add assertions to `test/integration/index_test.go` (or a new file
`test/integration/<lang>_test.go`) verifying:
- `stats.ByLang["yourlang"] > 0`
- at least one known symbol is findable via `reader.FindSymbol`
- references from the fixture resolve correctly

---

## 5. Update `.mycelium.yml` defaults

In [internal/config/config.go](../internal/config/config.go), add the new
language identifier to the default `Languages` list if it should be on by
default, and add a default `Include` glob pattern.

---

## Checklist

- [ ] `internal/parser/<lang>/parser.go` — implements `parser.Parser`
- [ ] `internal/parser/<lang>/doc.go` — package comment
- [ ] Registered in `cmd/myco/shared.go` (parser registry)
- [ ] (Optional) `internal/resolver/<lang>/resolver.go` — implements `pipeline.Resolver`
- [ ] (Optional) Registered in `cmd/myco/shared.go` (resolver map)
- [ ] Fixture added to `test/integration/testdata/fixtures/sample/`
- [ ] Integration test assertions added
- [ ] `go test -tags sqlite_fts5 -race ./test/integration/...` passes
- [ ] `task check` passes
