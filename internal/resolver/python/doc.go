// Package python is the v1.3 Python scope resolver.
//
// Python is dynamically typed, so we can't do what the Go resolver does
// with go/types. Instead we follow convention: imports + self. + module
// prefixes cover the vast majority of call sites in Python code.
//
// What we resolve:
//
//   import foo                       -> local "foo" = module "foo"
//   import foo as f                  -> local "f" = module "foo"
//   from foo import bar              -> local "bar" = symbol foo.bar
//   from foo import bar as b         -> local "b" = symbol foo.bar
//   from foo.sub import baz          -> local "baz" = symbol sub.baz
//   self.method(...)  inside class C -> resolves to C.method
//   cls.method(...)   inside class C -> resolves to C.method
//
// What we don't:
//
//   super().x()                      -> needs class hierarchy (out of scope)
//   getattr(obj, "m")(...)           -> dynamic, out of scope
//   type-based method dispatch       -> requires inference, out of scope
//
// ResolverVersion is 3 on every visited call, regardless of whether a
// qualified name got rewritten. This follows the v1.2 Go resolver's
// "visit vs rewrite" separation so builtins and dynamic calls don't
// inflate the truly-unresolved bucket.
package python
