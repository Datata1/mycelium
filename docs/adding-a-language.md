# Adding a language to Mycelium

Mycelium's parser layer is a small, stable interface. Adding a new language
touches exactly one wiring file — [internal/languages/languages.go](../internal/languages/languages.go)
— plus your new parser package and tests.

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
- Parsers emit plain structs and know nothing about storage — do not import
  `internal/index` or issue SQL.

Place the implementation in:

```
internal/parser/<lang>/parser.go
internal/parser/<lang>/doc.go       // package comment
```

Use tree-sitter for languages that have a grammar (`github.com/smacker/go-tree-sitter`).
Use stdlib AST packages where they exist (Go's `go/ast` is the canonical example — no cgo).

---

## 2. Register the parser in internal/languages

[internal/languages/languages.go](../internal/languages/languages.go) is the
single registration point — the daemon (`myco daemon`) and the one-shot
indexer (`myco index`) both build their registry there. Add one case to
`Registry`:

```go
case "yourlang":
    reg.Register(yourlang.New())
```

`internal/parser/registry.go` does first-match dispatch by `Supports`, so
registration order only matters when two parsers claim the same extension.

---

## 3. (Optional) Implement a Resolver

Raw textual refs (`ResolverVersion=0`) work for basic call graphs. For
precise cross-file resolution, implement `pipeline.Resolver`
([internal/pipeline/pipeline.go](../internal/pipeline/pipeline.go)):

```go
type Resolver interface {
    ResolveFile(ctx context.Context, absPath string, pr *parser.ParseResult) (resolved, total int)
    Ready() bool
}
```

`ResolveFile` rewrites `pr.References` in place, setting `DstName` to the
qualified form and bumping `ResolverVersion` to ≥ 1 on each rewritten ref.
`Ready()` returns false while the resolver is still loading (e.g. while
`go/packages` type-checks).

Resolvers that can compute type-inheritance edges additionally implement
`pipeline.InheritanceEmitter` — the pipeline detects it via type assertion,
so this is non-breaking.

Place the resolver in:

```
internal/resolver/<lang>/resolver.go
internal/resolver/<lang>/doc.go
```

Register it in `internal/languages/languages.go:Resolvers`:

```go
case "yourlang":
    out["yourlang"] = yourlangresolver.New()
```

---

## 4. Add tests

**Parser unit test** (cheap, fast feedback): a table-driven test in
`internal/parser/<lang>/parser_test.go` — source snippet in, expected
`Symbol`/`Reference` structs out. The existing parser tests double as
examples.

**Integration fixture**: add a minimal source file to

```
test/integration/testdata/fixtures/sample/<lang>/
```

then add assertions to `test/integration/index_test.go` (or a new
`test/integration/<lang>_test.go`) verifying:
- `stats.ByLang["yourlang"] > 0`
- at least one known symbol is findable via `reader.FindSymbol`
- references from the fixture resolve correctly

---

## 5. Update `.mycelium.yml` defaults

In [internal/config/config.go](../internal/config/config.go):
- add the identifier to `supportedLanguages` (config validation rejects
  unknown names),
- add it to the default `Languages` list if it should be on by default,
- add a default `Include` glob pattern.

---

## Checklist

- [ ] `internal/parser/<lang>/parser.go` — implements `parser.Parser`
- [ ] `internal/parser/<lang>/doc.go` — package comment
- [ ] Registered in `internal/languages/languages.go:Registry`
- [ ] (Optional) `internal/resolver/<lang>/resolver.go` — implements `pipeline.Resolver`
- [ ] (Optional) Registered in `internal/languages/languages.go:Resolvers`
- [ ] Parser unit test in `internal/parser/<lang>/parser_test.go`
- [ ] Fixture added to `test/integration/testdata/fixtures/sample/`
- [ ] Integration test assertions added
- [ ] `internal/config/config.go` — `supportedLanguages` + defaults updated
- [ ] `task check` passes
