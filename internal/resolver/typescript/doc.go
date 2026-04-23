// Package typescript is the v1.3 TypeScript / TSX scope resolver.
//
// Mirrors the Python resolver's shape. We don't run tsc — scope walking on
// top of the existing tree-sitter output is enough for the 80/20 cases
// most agent queries care about.
//
// What we resolve:
//
//   import { foo }   from "./bar"  -> local "foo" = bar.foo
//   import { foo as f } from "./bar" -> local "f" = bar.foo
//   import Bar       from "./bar"  -> local "Bar" = bar.Bar (default export proxy)
//   import * as ns   from "./bar"  -> local "ns"; ns.foo() -> bar.foo
//   this.method()  inside class C  -> resolves to C.method
//
// What we don't:
//
//   generics resolution              (explicit non-goal)
//   conditional types / infer        (explicit non-goal)
//   declaration merging              (explicit non-goal)
//   ambient modules beyond tsconfig.paths (explicit non-goal)
//   arbitrary obj.method() requiring type inference (stays textual)
//
// ResolverVersion is 2 on every visited call; the "visit-vs-rewrite"
// split matches the Go and Python resolvers.
package typescript
