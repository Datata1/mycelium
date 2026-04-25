package golang

import (
	"fmt"
	"go/ast"
	"go/token"
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

	// implements maps a concrete type's qualified name (e.g. "golang.Resolver")
	// to the interface qualifieds it satisfies. Populated alongside fileToPkg
	// during Load. Drives EmitInheritance and downstream interface-consumer
	// expansion in the query layer (v2.1, Chinthareddy 2026 §6.4).
	implements map[string][]string
	// fileImpls groups RefInherit edges by the absolute file path of the
	// concrete type's defining file, so EmitInheritance can append per-file
	// without scanning the universe each call.
	fileImpls map[string][]inheritEdge
}

// inheritEdge is a precomputed (concrete -> interface) pair waiting to be
// attached to the right ParseResult.
type inheritEdge struct {
	srcQualified string
	dstQualified string
	line         int
	col          int
}

// New constructs a resolver but does not load yet. Call Load() separately so
// callers can handle/surface load failures.
func New(modRoot string) *Resolver {
	return &Resolver{
		modRoot:    modRoot,
		fileToPkg:  map[string]*packages.Package{},
		implements: map[string][]string{},
		fileImpls:  map[string][]inheritEdge{},
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

	implements, fileImpls := computeImplementsGraph(pkgs, r.modRoot)

	r.mu.Lock()
	r.fileToPkg = fileToPkg
	r.loadErrs = loadErrs
	r.implements = implements
	r.fileImpls = fileImpls
	r.ready = true
	r.mu.Unlock()
	if debug {
		fmt.Fprintf(os.Stderr, "[resolver/go] inheritance graph: %d concrete types implement at least one interface\n",
			len(implements))
	}
	return len(loadErrs), nil
}

// computeImplementsGraph walks every loaded package, enumerates interfaces
// and concrete named types declared in PROJECT-LOCAL packages, and emits
// RefInherit-bound (concrete -> interface) edges for every satisfaction.
// Result drives interface-consumer expansion in the query layer (v2.1).
//
// Scope decisions:
//   - Both concretes AND interfaces must live under modRoot. Stdlib and
//     dep packages (golang.org/x/tools, etc.) aren't in our index, so
//     emitting edges to them produces orphan refs that ResolveRefs would
//     leave dangling. Restricting to project-local also keeps the noise
//     down — Go's structural interfaces are extremely permissive.
//   - The empty interface and aliases are excluded — they'd inflate the
//     graph without adding signal.
//   - We also exclude the concrete that IS the interface (degenerate self-
//     loop when iface satisfies its own decl).
func computeImplementsGraph(pkgs []*packages.Package, modRoot string) (map[string][]string, map[string][]inheritEdge) {
	type ifaceInfo struct {
		qualified string // e.g. "pipeline.Resolver"
		t         *types.Interface
	}
	type concreteInfo struct {
		qualified string // e.g. "golang.Resolver"
		named     *types.Named
		definedAt token.Pos
	}

	var ifaces []ifaceInfo
	var concretes []concreteInfo

	absModRoot, _ := filepath.Abs(modRoot)

	// First pass: collect every named type in PROJECT-LOCAL packages only.
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types == nil {
			return
		}
		if !packageUnderRoot(p, absModRoot) {
			return
		}
		scope := p.Types.Scope()
		shortPkg := p.Types.Name()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			qualified := shortPkg + "." + tn.Name()
			switch underlying := named.Underlying().(type) {
			case *types.Interface:
				if underlying.Empty() {
					continue
				}
				ifaces = append(ifaces, ifaceInfo{qualified: qualified, t: underlying})
			default:
				_ = underlying
				concretes = append(concretes, concreteInfo{
					qualified: qualified,
					named:     named,
					definedAt: tn.Pos(),
				})
			}
		}
	})

	// Index file positions so we can attach edges to the concrete's defining
	// file. Build a map from package -> fset for line/col lookups.
	posToFile := map[token.Pos]string{}
	posToLineCol := map[token.Pos][2]int{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Fset == nil {
			return
		}
		for _, c := range concretes {
			if c.definedAt == token.NoPos {
				continue
			}
			pos := p.Fset.Position(c.definedAt)
			if pos.Filename == "" {
				continue
			}
			abs, err := filepath.Abs(pos.Filename)
			if err != nil {
				continue
			}
			posToFile[c.definedAt] = abs
			posToLineCol[c.definedAt] = [2]int{pos.Line, pos.Column}
		}
	})

	implements := map[string][]string{}
	fileImpls := map[string][]inheritEdge{}
	// Dedupe (concrete, interface) pairs because packages.Load(Tests: true)
	// surfaces each package once for the regular variant and once for the
	// test variant — the same named type would otherwise produce two edges.
	type pair struct{ src, dst string }
	seenPair := map[pair]struct{}{}

	// Second pass: for every (concrete, interface) pair, run types.Implements.
	// types.Implements handles pointer receivers and embedded methods; we
	// check both T and *T because Go's method set rules differ.
	for _, c := range concretes {
		concrete := c.named
		ptr := types.NewPointer(concrete)
		for _, i := range ifaces {
			if c.qualified == i.qualified {
				continue
			}
			if !types.Implements(concrete, i.t) && !types.Implements(ptr, i.t) {
				continue
			}
			key := pair{c.qualified, i.qualified}
			if _, dup := seenPair[key]; dup {
				continue
			}
			seenPair[key] = struct{}{}
			implements[c.qualified] = append(implements[c.qualified], i.qualified)
			absPath, ok := posToFile[c.definedAt]
			if !ok {
				continue
			}
			lc := posToLineCol[c.definedAt]
			fileImpls[absPath] = append(fileImpls[absPath], inheritEdge{
				srcQualified: c.qualified,
				dstQualified: i.qualified,
				line:         lc[0],
				col:          lc[1],
			})
		}
	}
	return implements, fileImpls
}

// EmitInheritance appends RefInherit edges for the file's concrete types
// to pr.References. Each edge is `concrete -> interface`, marked with
// ResolverVersion=1 so downstream resolution treats it as authoritative.
//
// No-op when the resolver isn't loaded or the file isn't in any loaded
// package. Safe to call on every file in the pipeline.
func (r *Resolver) EmitInheritance(absPath string, pr *parser.ParseResult) int {
	r.mu.RLock()
	ready := r.ready
	edges := r.fileImpls[absPath]
	r.mu.RUnlock()
	if !ready || len(edges) == 0 {
		return 0
	}
	// Build a fast set of qualifieds defined in this file so we only emit
	// edges whose source is actually a symbol the parser produced. Avoids
	// orphan refs that ResolveRefs would later strand.
	parserSyms := map[string]struct{}{}
	for _, s := range pr.Symbols {
		parserSyms[s.Qualified] = struct{}{}
	}
	added := 0
	for _, e := range edges {
		if _, ok := parserSyms[e.srcQualified]; !ok {
			continue
		}
		pr.References = append(pr.References, parser.Reference{
			SrcSymbolQualified: e.srcQualified,
			DstName:            e.dstQualified,
			Kind:               parser.RefInherit,
			Line:               e.line,
			Col:                e.col,
			ResolverVersion:    ResolverVersion,
		})
		added++
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[resolver/go] inheritance edges %s: added=%d\n", pr.Path, added)
	}
	return added
}

// ImplementsCount returns the number of concrete types with at least one
// interface implementation recorded. Surfaces via doctor as
// `interface_expansion_coverage`.
func (r *Resolver) ImplementsCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.implements)
}

// packageUnderRoot returns true when at least one of the package's source
// files lives under absRoot. Used to scope inheritance computation to
// project-local packages — we don't want to emit edges to stdlib or dep
// interfaces that don't appear in our symbol table.
func packageUnderRoot(p *packages.Package, absRoot string) bool {
	if absRoot == "" {
		return true
	}
	files := p.GoFiles
	if len(files) == 0 {
		files = p.CompiledGoFiles
	}
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			continue
		}
		if strings.HasPrefix(abs, absRoot+string(filepath.Separator)) || abs == absRoot {
			return true
		}
	}
	return false
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
