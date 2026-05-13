# C1 — Route literals as a symbol kind

**Priority:** P0 for v4 Phase 3 — main user-visible v4 feature
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Depends on:** v4-f1 (Django routes data), v4-f2 (Axum routes data)

## Goal

Make framework route literals indexable + queryable. Today an agent
looking for "what handles `/api/users/:id`?" falls back to grep
because routes aren't symbols — they're string arguments to
framework registration functions. C1 adds `kind: "route"` symbols
emitted by the language parsers when they see a configured route
constructor pattern, plus a new MCP tool `find_route(pattern)` for
substring lookup.

After this ticket: `find_route("/api/users")` returns a list of
`Route{pattern, handler_path, handler_line, framework}` matches.

## What changes

### Schema — additive, no migration

`symbols.kind` is a free-text column. Adding the value `"route"`
needs no migration. The new field is `route.handler_symbol_id`
(optional, nullable) which links a route symbol to its handler
function — that's the v3.x-style `refs` row, no new table needed.
Source-side: the route symbol's `signature` field carries the
framework name (`"django.urls.path"`, `"axum.Router.route"`, etc.)
so callers can filter without joining.

### Per-language config

```yaml
# .mycelium.yml
languages:
  python:
    route_constructors:
      - pattern: "django.urls.path"
        framework: django
        # First positional arg is the route literal; second is the handler.
      - pattern: "django.urls.re_path"
        framework: django
      - pattern: "fastapi.FastAPI.get"
        framework: fastapi
        # The decorator form: @app.get("/foo") — handled by C1's TS-style
        # decorator detection.
  rust:
    route_constructors:
      - pattern: "axum.Router.route"
        framework: axum
        # Method-chain form: Router::new().route("/foo", get(handler))
  typescript:
    route_constructors:
      - pattern: "@tanstack/react-router.createFileRoute"
        framework: tanstack-router
      - pattern: "next.NextRequest.handler" # placeholder; F1/F2 confirm shape
        framework: next
```

The default `route_constructors` list ships **off** by default;
users opt in per-repo. Enabling globally for one framework is a
2-line config change.

### Parser changes

For each supported language (Go, TS, Python; Rust if C2 lands
first):

- Walker tracks calls / decorators matching `route_constructors`.
- On a match, emit a synthetic symbol:
  ```
  Symbol{
    Name: "<route literal as written>",        // e.g. "/api/users/{id}"
    Qualified: "<framework>.<route literal>",
    Kind: "route",
    Path: <file path>,
    StartLine: <line of the route call>,
    EndLine: <same>,
    Signature: "<framework>",
    Docstring: "<handler symbol qualified name, if resolvable>",
  }
  ```
- Emit a `ref` row from the route symbol to the handler symbol
  when the handler is statically resolvable (e.g.
  `path("/foo", views.index)` → ref to `views.index`).

Tree-sitter queries land alongside the existing parser code; for
Go's stdlib parser, add a second pass over `*ast.CallExpr` nodes.

### Query layer

- `internal/query/routes.go` (new file) — `FindRoute(ctx, pattern,
  framework, project, limit) ([]RouteMatch, error)`. Substring
  match on `name` filtered to `kind = 'route'`.
- `RouteMatch` struct includes the handler symbol if resolved.

### MCP tool

- `pkg/mcpschema/tools.go` — new `find_route` tool with description:
  > "Find framework route literals by substring. Reach for this
  > **before** `search_lexical` whenever the question is 'what
  > handles `/path/here`?'. Returns the route symbol + its handler
  > function (when statically resolvable). Configure via
  > `languages.<lang>.route_constructors:` in `.mycelium.yml`."
- `internal/ipc/proto.go` — `FindRouteParams{Pattern, Framework,
  Project, Limit}` + `MethodFindRoute = "find_route"`.
- `internal/daemon/daemon.go` — dispatch case, calls into
  `query.FindRoute`.
- `cmd/myco/main.go` — `myco query route <pattern>` subcommand
  (mirrors `myco query find <name>`).

## Critical files

- `internal/parser/python/parser.go` — Django constructor detection.
- `internal/parser/typescript/parser.go` — TanStack Router /
  Next.js detection.
- `internal/parser/golang/parser.go` — Cobra `Use:` is
  arguably a route literal too; document but skip for v4 unless
  F1/F2 surfaces demand.
- `internal/config/config.go` — `LanguageConfig.RouteConstructors`
  field type.
- `internal/query/routes.go` — new query method.
- `internal/ipc/proto.go`, `internal/daemon/daemon.go`,
  `pkg/mcpschema/tools.go`, `cmd/myco/main.go` — wire the new
  MCP tool through all transports.
- `internal/index/migrations/` — **no migration** required (kind
  is free-text). Document this in CHANGELOG.

## Acceptance criteria

- `task check` passes.
- Test fixtures in `internal/parser/python/testdata/django_routes.py`
  and `internal/parser/typescript/testdata/tanstack_routes.tsx`
  exercise the parser end-to-end.
- `find_route("/api/users")` against the F1 Django repo returns
  every URL pattern the F1 findings doc lists as agent-grepped-for.
  Same shape against F2's Axum repo (when C2 lands).
- The route symbol's `Docstring` (= handler symbol qualified name)
  is populated when the handler is a same-file function reference;
  empty when it's a string-based view path Django still allows
  (document this limitation).
- New unit tests pin the wire shape (`FindRouteParams`,
  `RouteMatch` JSON shape) so MCP clients don't break on rename.
- `myco query route /api` CLI form prints a one-route-per-line
  table (path, framework, handler).
- README + LIMITATIONS.md updated: route literals removed from
  "what we don't claim", added to feature list.

## What this enables

- **The F4 finding from monorepo-4 is closed.** Agents reaching
  for "what handles this route" get a structured answer.
- **Cross-framework consistency.** A user with Django + Next.js
  in one workspace gets the same `find_route` shape across both.
- **Foundation for `impact_analysis(target=route)`** — bumps
  reachability from a route to its full call graph. Already
  works as soon as the route → handler ref lands.

## Out of scope

- **Dynamic route patterns.** `path(prefix + "/api", ...)` where
  the literal is concatenated at runtime — out of scope, falls
  back to text search. Document.
- **Route uniqueness / overlap detection** — "warn when two
  routes match the same URL". v4.1+ tooling on top of the
  symbol surface.
- **Method-level routing** (GET vs POST). C1 emits one symbol per
  route literal, not per (route, method). The framework name in
  Signature distinguishes a `axum.Router.route` from `axum.Router.get`
  but the user-facing query treats them identically.
- **Cobra subcommands as routes.** Skipped for v4 unless field
  tests prove demand. Cobra `cmd.Use` is already
  `find_symbol`-able by name.

## Honest caveats

- The `route_constructors` list will get out of date as
  frameworks evolve. Default-off + community-PR'd config snippets
  is the maintenance model. Worth a `docs/route-config.md` page
  with copy-paste blocks for the popular frameworks (next two
  weeks of v4 work, not blocking).
- Static resolution of the handler argument is best-effort. When
  Django's `path("/foo", views.index)` references an imported
  symbol, the existing resolver chain handles it. When it's a
  string-based view path (`path("/foo", "myapp.views.index")`),
  resolution skips — document.
