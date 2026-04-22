package golang

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/jdwiederstein/mycelium/internal/parser"
)

// debug is flipped on by MYCELIUM_RESOLVER_DEBUG=1; useful when diagnosing
// why resolution isn't landing as expected. Kept as a plain package var
// rather than a flag so existing binaries can turn it on without a rebuild.
var debug = os.Getenv("MYCELIUM_RESOLVER_DEBUG") == "1"

// ResolverVersion is the marker we stamp on refs this package successfully
// resolves. Bumping this invalidates cached resolutions — v1.2 ships at 1.
const ResolverVersion = 1

// Resolver caches a module's *packages.Package graph and answers
// ResolveFile(pr) by rewriting call refs in place.
type Resolver struct {
	modRoot string

	mu    sync.RWMutex
	ready bool
	// fileToPkg maps absolute Go file paths to the loaded package.
	// Populated once at Load; callers that see an unknown path fall back
	// to textual resolution (no error).
	fileToPkg map[string]*packages.Package
	// errors collected at load time; exposed via LoadErrors() for doctor.
	loadErrs []error
}

// New constructs a resolver but does not load yet. Call Load() separately so
// callers can handle/surface load failures.
func New(modRoot string) *Resolver {
	return &Resolver{
		modRoot:   modRoot,
		fileToPkg: map[string]*packages.Package{},
	}
}

// Load runs `go/packages` once across the whole module (./...). Safe to call
// multiple times; the second call replaces the cache atomically. Returns
// the aggregate error count; a non-zero count doesn't abort — partial type
// info is still used for packages that did type-check.
func (r *Resolver) Load() (int, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedImports |
			packages.NeedDeps,
		Dir:   r.modRoot,
		Tests: true, // include _test.go files; otherwise integration and
		// bench tests produce a large cluster of "unresolved" calls that
		// are actually just invisible to the loader.
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		// Hard failure (bad module, missing go.mod). Leave the resolver in
		// "not ready" state so ResolveFile becomes a no-op.
		r.mu.Lock()
		r.ready = false
		r.loadErrs = []error{err}
		r.mu.Unlock()
		return 0, fmt.Errorf("packages.Load: %w", err)
	}

	fileToPkg := map[string]*packages.Package{}
	var loadErrs []error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, file := range p.CompiledGoFiles {
			fileToPkg[file] = p
		}
		for _, file := range p.GoFiles {
			fileToPkg[file] = p
		}
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, fmt.Errorf("%s: %s", p.PkgPath, e.Msg))
		}
	})

	r.mu.Lock()
	r.fileToPkg = fileToPkg
	r.loadErrs = loadErrs
	r.ready = true
	r.mu.Unlock()
	return len(loadErrs), nil
}

// LoadErrors returns type-check errors collected during the last Load().
// Surfaced via `myco doctor` so users can see which packages are degrading
// resolution quality.
func (r *Resolver) LoadErrors() []error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]error, len(r.loadErrs))
	copy(out, r.loadErrs)
	return out
}

// Ready returns whether the resolver has been loaded. A not-ready resolver
// leaves every call to textual fallback.
func (r *Resolver) Ready() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ready
}

// FileCount is the number of Go files we have cached type info for. Doctor
// surfaces this alongside LoadErrors() so users can tell "loaded nothing"
// from "loaded 100 files with 5 package errors."
func (r *Resolver) FileCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.fileToPkg)
}

// ResolveFile is the main entry point. It mutates pr.References in place,
// setting DstName to a qualified form and ResolverVersion=1 for every call
// ref we can resolve. Returns (resolved, total) counts.
//
// Safe to call from multiple goroutines as long as each goroutine passes a
// different ParseResult.
func (r *Resolver) ResolveFile(absPath string, pr *parser.ParseResult) (resolved, total int) {
	r.mu.RLock()
	ready := r.ready
	pkg := r.fileToPkg[absPath]
	r.mu.RUnlock()
	if !ready || pkg == nil || pkg.TypesInfo == nil {
		if debug {
			fmt.Fprintf(os.Stderr, "[resolver/go] skip %s: ready=%v pkg=%v\n", absPath, ready, pkg != nil)
		}
		return 0, 0
	}

	file := findFile(pkg, absPath)
	if file == nil {
		if debug {
			fmt.Fprintf(os.Stderr, "[resolver/go] file not in package: %s\n", absPath)
		}
		return 0, 0
	}

	// Chained calls like `tx.QueryRowContext(...).Scan(&id)` produce two
	// CallExprs whose .Pos() coincide (both at the `tx` token), and the
	// parser emits one ref per call. So we key on (line, col, short-name)
	// instead of just (line, col) — the short-name (parser-side
	// callTargetName tail) disambiguates the pair.
	type refKey struct {
		line, col int
		short     string
	}
	callRefs := map[refKey]int{}
	for i, ref := range pr.References {
		if ref.Kind == parser.RefCall {
			callRefs[refKey{ref.Line, ref.Col, lastSegment(ref.DstName)}] = i
			total++
		}
	}

	var rewrote, visited, missedPos int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pos := pkg.Fset.Position(call.Pos())
		parserName := parserCallName(call.Fun)
		key := refKey{pos.Line, pos.Column, lastSegment(parserName)}
		idx, ok := callRefs[key]
		if !ok {
			missedPos++
			return true
		}
		// ResolverVersion=1 means "the resolver visited this call" —
		// independent of whether we could rewrite DstName. Builtins
		// (len, make, append), type conversions (int(x)), and interface
		// calls with erased receivers stay at the parser's textual
		// DstName but are flagged as type-seen so they don't inflate the
		// truly-unresolved bucket.
		pr.References[idx].ResolverVersion = ResolverVersion
		visited++
		resolved++
		if qualified := qualifyCall(call, pkg.TypesInfo); qualified != "" {
			pr.References[idx].DstName = qualified
			rewrote++
		}
		return true
	})
	if debug {
		fmt.Fprintf(os.Stderr, "[resolver/go] %s: visited=%d rewrote=%d total_calls=%d missedPos=%d\n",
			pr.Path, visited, rewrote, total, missedPos)
	}
	return resolved, total
}

// parserCallName mirrors internal/parser/golang.callTargetName so we can
// match AST call positions against parser-emitted refs even when two calls
// share a position (chained call sites). Keep them in sync.
func parserCallName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		base := parserCallName(x.X)
		if base == "" {
			return x.Sel.Name
		}
		return base + "." + x.Sel.Name
	}
	return ""
}

// lastSegment returns the suffix after the final "." — matches the
// dst_short column the index maintains for parser refs.
func lastSegment(dotted string) string {
	for i := len(dotted) - 1; i >= 0; i-- {
		if dotted[i] == '.' {
			return dotted[i+1:]
		}
	}
	return dotted
}

func findFile(pkg *packages.Package, absPath string) *ast.File {
	// Normalize both sides so symlinks / trailing-slash differences don't bite.
	want, _ := filepath.Abs(absPath)
	for i, f := range pkg.CompiledGoFiles {
		if eqPath(f, want) && i < len(pkg.Syntax) {
			return pkg.Syntax[i]
		}
	}
	for i, f := range pkg.GoFiles {
		if eqPath(f, want) && i < len(pkg.Syntax) {
			return pkg.Syntax[i]
		}
	}
	return nil
}

func eqPath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return aa == bb
}

// qualifyCall returns the "pkg.Receiver.Method" or "pkg.Func" form for a
// call expression, or "" when we can't or shouldn't attribute it.
func qualifyCall(call *ast.CallExpr, info *types.Info) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return qualifyObject(info.Uses[fn])
	case *ast.SelectorExpr:
		// Method call on an expression (r.FindSymbol, foo.Bar().Baz())
		if sel, ok := info.Selections[fn]; ok {
			return qualifyObject(sel.Obj())
		}
		// Package-qualified call like `fmt.Println` — Selections is nil
		// because it's not a *Selection; the Sel identifier is in Uses.
		return qualifyObject(info.Uses[fn.Sel])
	}
	return ""
}

// qualifyObject renders a types.Object in the parser's qualified-name shape.
// We deliberately match what internal/parser/golang.extractFunc produces so
// the SQL qualified-match in index.ResolveRefs lands the join.
func qualifyObject(obj types.Object) string {
	if obj == nil {
		return ""
	}
	// Builtins (`len`, `make`, `new`, ...) — no package. The parser doesn't
	// emit them as symbols, so a qualified name would point nowhere. Skip
	// them and let the textual fallback handle (it also won't match; that's
	// the honest outcome for a builtin).
	pkg := obj.Pkg()
	if pkg == nil {
		return ""
	}
	shortPkg := pkg.Name()

	fn, isFunc := obj.(*types.Func)
	if !isFunc {
		// Variable, const, type: qualify as pkg.Name. These rarely appear
		// as "call targets" (a var-holding-a-func would), but cover the
		// case for completeness.
		return shortPkg + "." + obj.Name()
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		// Top-level function.
		return shortPkg + "." + fn.Name()
	}

	recv := baseTypeName(sig.Recv().Type())
	if recv == "" {
		// Couldn't peel the receiver (e.g. generic witness type). Fall back
		// to package-qualified only; still better than raw textual.
		return shortPkg + "." + fn.Name()
	}
	return shortPkg + "." + recv + "." + fn.Name()
}

// baseTypeName strips pointers and type instantiations down to the bare
// named type. Matches the parser's receiverTypeName.
func baseTypeName(t types.Type) string {
	for {
		switch tt := t.(type) {
		case *types.Pointer:
			t = tt.Elem()
		case *types.Named:
			return tt.Obj().Name()
		case *types.Alias:
			return tt.Obj().Name()
		case *types.TypeParam:
			return tt.Obj().Name()
		default:
			if s := fallbackTypeName(t); s != "" {
				return s
			}
			return ""
		}
	}
}

// fallbackTypeName handles exotic receivers that don't fit the straightforward
// pointer/named unwrapping above. We use String() as a last resort and pluck
// the trailing identifier.
func fallbackTypeName(t types.Type) string {
	s := t.String()
	if i := strings.LastIndexAny(s, ".*"); i >= 0 {
		return s[i+1:]
	}
	return s
}
