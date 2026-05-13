# BUG (P0) — `find_symbol` returns null on TS type aliases in `.d.ts` files

**Priority:** P0 for v4 — adoption blocker on TS codebases
**Surfaced by:** `tickets/v4-f1-findings.md` (T1)
**Plan:** `~/.claude/plans/10-v4-agent-native-completed.md`
**Blocks:** Phase 2 Python/Django F1 retest (likely same shape on
Python `.pyi` stub files); v4.0 release.

## Problem

In Codesphere `monorepo-4`, the agent called
`find_symbol{name: "WorkspacePlan"}`. Result: `null`. But
`WorkspacePlan` is a TypeScript type defined in
`packages/payment-service/common/lib/Product.d.ts` and re-exported /
re-imported across dozens of files in the monorepo. A grep finds it
instantly.

The agent then fell through to `Bash/grep` and `Read` for the rest
of the session. Every TS user navigating to a third-party type, an
ambient declaration, or any of the *many* type aliases that live in
`.d.ts` files in modern TS codebases will hit this same dead end.

This is the **load-bearing TS adoption blocker** v4 has to fix
before the route-literal + new-language story (Phase 3) can claim
to deliver v4's "TypeScript users get the agent-native experience"
goal.

## What's wrong (hypothesis)

The TS parser at `internal/parser/typescript/parser.go` uses
tree-sitter-typescript. Two plausible failure modes:

1. **`.d.ts` files aren't picked up by the walker.** The repo
   walker uses an include glob (`src/**/*.{ts,tsx}` in this user's
   `.mycelium.yml`); declaration files in `lib/` or `dist/` may be
   excluded. Even with a broader glob, the daemon may treat `.d.ts`
   files specially and skip parsing.
2. **`.d.ts` files are walked but the parser drops the symbols.**
   tree-sitter-typescript parses `.d.ts` (it's the same grammar
   under `tsx: false` mode), but the walker may be filtering for
   only function/class/method symbols and dropping `interface` /
   `type` declarations — exactly the kinds that dominate `.d.ts`.

The fix depends on which mode is the actual cause. Verify first.

## Verification (do this before coding)

Run on the affected repo (`monorepo-4` works as the test bed):

```bash
# (a) Are .d.ts files indexed at all?
myco query files --json | jq '[.[] | select(.path | endswith(".d.ts"))] | length'
# Expected if (1): 0
# Expected if (2): >0 but symbol_count == 0 for them
```

```bash
# (b) Is WorkspacePlan in the index under any kind?
myco stats --json | jq '.by_kind'
# Look for "type" / "interface" presence
```

```bash
# (c) Direct DB probe (read-only, daemon must be up):
sqlite3 .mycelium/index.db \
  "SELECT path, symbol_count FROM files WHERE path LIKE '%.d.ts' LIMIT 10"
sqlite3 .mycelium/index.db \
  "SELECT name, qualified, kind, path FROM symbols WHERE name='WorkspacePlan'"
```

The verify step decides whether the fix lives in the include-glob
defaults, the walker, or the parser symbol filter. **Don't write
code until you know which one.**

## What changes (after verification)

If mode (1) — walker skips `.d.ts`:
- `internal/parser/parser.go` (or wherever the language→extension
  registration lives): ensure `.d.ts` registers under `typescript`.
- `internal/repo/walker.go` (or wherever default includes live): if
  there's a hard-coded exclude for `.d.ts`, remove it.
- `myco init` wizard's default `.mycelium.yml` template: include
  `**/*.d.ts` in the TS glob.

If mode (2) — parser drops type declarations:
- `internal/parser/typescript/walker.go`: locate the symbol-emit
  switch; ensure `interface_declaration`, `type_alias_declaration`,
  and `enum_declaration` (the three big `.d.ts` shapes) emit
  symbols with `kind: "type"`.
- `internal/parser/typescript/testdata/`: add a `.d.ts` fixture
  with a representative shape (interface + type alias + ambient
  declaration) and a snapshot test.

Either way:
- Existing TS test fixtures (`internal/parser/typescript/testdata/`)
  should grow a `*.d.ts` fixture so a regression here surfaces in
  unit tests, not just field tests.
- `internal/index/migrations/`: no schema change needed (`.kind` is
  free-text, `type` already exists).

## Critical files

- `internal/parser/typescript/parser.go` — entry point.
- `internal/parser/typescript/walker.go` — the AST walker.
- `internal/parser/typescript/testdata/` — fixtures.
- `internal/parser/parser.go` — language registry.
- `internal/repo/walker.go` — file walker (if include/exclude
  needs adjustment).
- `internal/wizard/` — default config template generator.

## Acceptance criteria

- `task check` passes.
- New fixture in `internal/parser/typescript/testdata/` exercises a
  `.d.ts` file with an `interface`, a `type` alias, and an enum;
  snapshot test asserts the emitted symbols.
- Re-run on `monorepo-4`: `myco find symbol WorkspacePlan` returns
  ≥ 1 hit, with `path` ending in `.d.ts`.
- The integration test in `integration_test.go` gets a TS `.d.ts`
  case so the fix doesn't silently regress.
- `myco doctor` on `monorepo-4` continues to show <1% unresolved
  refs (the fix shouldn't tank resolution by adding noisy
  `.d.ts`-only symbols that have no matching usages).

## What this enables

- **TS users get find_symbol on every type alias.** The single
  largest find_symbol-failure surface in TS codebases closes.
- **The v4 F1 re-run produces a positive savings number.** T1 +
  T2 fixed will let the agent actually use the index for TS
  navigation, which is the v4.0 release acceptance number.
- **Python `.pyi` stub-file follow-up.** The same shape almost
  certainly applies to Python — fix the framework here, replicate
  the fixture for Python in a follow-up.

## Out of scope

- **JSDoc-typed JavaScript files** (`@typedef` patterns in `.js`).
  Different parser surface; defer to v4.1+ unless F1 surfaces it.
- **Generated `.d.ts` from `.proto` / `openapi`.** Treated as
  regular `.d.ts` after the fix; the generator-specific structure
  is out of scope.
- **Ambient `declare module` statements** that re-export across
  package boundaries. The fix should index the symbols; the
  cross-module re-export resolution is a v4.1+ resolver follow-up.

## Honest caveats

- The fix may add a non-trivial number of low-value symbols
  (every utility type from every `lib/` directory). If unresolved-
  ref ratio degrades meaningfully on large repos, gate `.d.ts`
  symbol emission behind a config flag (`languages.typescript.
  index_dts: true` default false → flip to true once we've measured
  the symbol-table inflation).
- Some `.d.ts` files are large (`@types/node` is megabytes). The
  walker's `max_file_size_kb` config (default 1024) may already
  exclude them; document this so users know why a specific stub
  doesn't appear.
- This bug only surfaces when an agent reaches for `find_symbol`
  on a `.d.ts`-resident type. Adoption is partly self-correcting
  (the agent that hit the null fell back to grep), so the
  *visible* impact is tool-disuse, not crashes. That makes it
  easier to under-prioritise — don't.
