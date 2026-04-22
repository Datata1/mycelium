// Package golang resolves Go call-site references against full type info.
//
// It uses golang.org/x/tools/go/packages to load a module once at
// construction, then rewrites parser.Reference.DstName with fully-qualified
// targets that the existing index.ResolveRefs qualified-match pass can link
// to a symbol row.
//
// Resolution stays intentionally per-file: ResolveFile() walks one parsed
// AST (via the cached package the file belongs to) and mutates the caller's
// ParseResult in place. Calls whose callees type-check cleanly get
// ResolverVersion=1 and a DstName of the form "pkg.Receiver.Method" (same
// shape the Go parser produces for its own symbols). Calls we can't resolve
// — builtins, type errors, interface method calls whose object vanished —
// are left alone; the downstream SQL resolver's short-name pass still has a
// shot at them.
//
// Failure mode: if go/packages.Load fails outright (not a Go module,
// transient compile break the loader can't work around), New returns a
// resolver whose ResolveFile is a no-op. Callers never see an error from
// ResolveFile — type-aware resolution is always best-effort; the textual
// fallback is the floor.
package golang
