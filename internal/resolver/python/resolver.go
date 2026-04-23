package python

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"

	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/tsutil"
)

// ResolverVersion tags every call the Python resolver visits.
const ResolverVersion = 3

// Resolver is a stateless per-file resolver; it re-parses each file we
// resolve against (the tree-sitter parse is cheap compared to go/packages)
// instead of caching ASTs. Keeping it stateless sidesteps staleness bugs
// in the watcher hot path.
type Resolver struct {
	once sync.Once
	lang *sitter.Language
}

func New() *Resolver { return &Resolver{} }

// Ready is always true — we don't have an up-front load step like go/packages.
func (r *Resolver) Ready() bool { return true }

func (r *Resolver) init() { r.lang = python.GetLanguage() }

// ResolveFile walks the file a second time, builds scope + import tables,
// and rewrites call DstNames where it can.
func (r *Resolver) ResolveFile(absPath string, pr *parser.ParseResult) (resolved, total int) {
	r.once.Do(r.init)

	// Re-parse to get a tree-sitter AST we can walk. The parser did this
	// once already; we pay for it again to keep the resolver independent
	// (v1.4+ can optimize by letting the parser pass the tree through).
	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(r.lang)
	// Read the file directly. If that fails we bail; pr still has textual refs.
	content, err := readFile(absPath)
	if err != nil {
		return 0, 0
	}
	tree, err := p.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return 0, 0
	}
	defer tree.Close()

	module := moduleName(absPath)
	table := buildImportTable(tree.RootNode(), content)
	classMethods := buildClassMethods(tree.RootNode(), content, module)

	// Index parser refs by (line, col, short-name) — same trick as the Go
	// resolver uses for chained calls.
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

	// Walk every `call` node in the AST, resolve, mark visited.
	tsutil.Walk(tree.RootNode(), func(n *sitter.Node) bool {
		if n.Type() != "call" {
			return true
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return true
		}
		parserName := parserCallName(content, fn)
		sl, sc, _, _ := tsutil.Position(n)
		idx, ok := callRefs[refKey{sl, sc, lastSegment(parserName)}]
		if !ok {
			return true
		}
		// Visit-and-mark, same semantics as the Go resolver's v1.2
		// refinement: every call the resolver reached gets the resolver
		// version, even if we couldn't qualify it.
		pr.References[idx].ResolverVersion = ResolverVersion
		resolved++
		if q := resolveCall(content, fn, table, classMethods, enclosingClass(n, content), module); q != "" {
			pr.References[idx].DstName = q
		}
		return true
	})
	return resolved, total
}

// --- import + scope tables --------------------------------------------------

type importBinding struct {
	// Module name the binding refers to (short — matches parser's module()).
	Module string
	// Remote symbol name inside Module, "" for whole-module imports.
	Remote string
}

// buildImportTable walks top-level import statements and returns a map from
// local binding name to its import source.
func buildImportTable(root *sitter.Node, content []byte) map[string]importBinding {
	out := map[string]importBinding{}
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(int(i))
		switch n.Type() {
		case "import_statement":
			addImportStatement(n, content, out)
		case "import_from_statement":
			addFromImport(n, content, out)
		}
	}
	return out
}

func addImportStatement(n *sitter.Node, content []byte, out map[string]importBinding) {
	// import a, b, c as cc
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(int(i))
		switch c.Type() {
		case "dotted_name":
			mod := shortModule(tsutil.Slice(content, c))
			out[mod] = importBinding{Module: mod}
		case "aliased_import":
			nameNode := c.ChildByFieldName("name")
			aliasNode := c.ChildByFieldName("alias")
			if nameNode == nil || aliasNode == nil {
				continue
			}
			mod := shortModule(tsutil.Slice(content, nameNode))
			alias := tsutil.Slice(content, aliasNode)
			out[alias] = importBinding{Module: mod}
		}
	}
}

func addFromImport(n *sitter.Node, content []byte, out map[string]importBinding) {
	// from foo.bar import baz, qux as q
	mod := ""
	if src := n.ChildByFieldName("module_name"); src != nil {
		mod = shortModule(tsutil.Slice(content, src))
	}
	if mod == "" {
		return
	}
	// Named children after the module are the import targets. Iterate
	// explicitly skipping the module child.
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(int(i))
		switch c.Type() {
		case "dotted_name":
			// First "dotted_name" is the module itself; skip.
			if isSameNode(c, n.ChildByFieldName("module_name")) {
				continue
			}
			name := tsutil.Slice(content, c)
			out[name] = importBinding{Module: mod, Remote: name}
		case "aliased_import":
			nameNode := c.ChildByFieldName("name")
			aliasNode := c.ChildByFieldName("alias")
			if nameNode == nil || aliasNode == nil {
				continue
			}
			out[tsutil.Slice(content, aliasNode)] = importBinding{Module: mod, Remote: tsutil.Slice(content, nameNode)}
		}
	}
}

// buildClassMethods returns {className: {methodName: qualified}} for every
// class definition in the file. Used to resolve self.method() calls.
// Caller provides the module short-name so qualified names match what the
// Python parser emits for the class's symbol rows.
func buildClassMethods(root *sitter.Node, content []byte, module string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(int(i))
		if n.Type() != "class_definition" {
			continue
		}
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		className := tsutil.Slice(content, nameNode)
		methods := map[string]string{}
		body := n.ChildByFieldName("body")
		if body == nil {
			out[className] = methods
			continue
		}
		for j := uint32(0); j < body.NamedChildCount(); j++ {
			c := body.NamedChild(int(j))
			var fn *sitter.Node
			switch c.Type() {
			case "function_definition":
				fn = c
			case "decorated_definition":
				// Find the underlying function_definition.
				for k := uint32(0); k < c.NamedChildCount(); k++ {
					if x := c.NamedChild(int(k)); x.Type() == "function_definition" {
						fn = x
						break
					}
				}
			}
			if fn == nil {
				continue
			}
			nm := fn.ChildByFieldName("name")
			if nm == nil {
				continue
			}
			methodName := tsutil.Slice(content, nm)
			methods[methodName] = module + "." + className + "." + methodName
		}
		out[className] = methods
	}
	return out
}

// enclosingClass returns the name of the class containing this node, or "".
// Walks the parent chain in the tree-sitter tree.
func enclosingClass(n *sitter.Node, content []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_definition" {
			if nm := p.ChildByFieldName("name"); nm != nil {
				return tsutil.Slice(content, nm)
			}
		}
	}
	return ""
}

// --- call target resolution ------------------------------------------------

// resolveCall translates a call.Fun expression into a qualified name using
// the import table + class scope. Returns "" when we can't qualify;
// caller leaves DstName as the parser's textual target.
func resolveCall(content []byte, fn *sitter.Node, imports map[string]importBinding, classMethods map[string]map[string]string, currentClass, selfModule string) string {
	switch fn.Type() {
	case "identifier":
		name := tsutil.Slice(content, fn)
		if b, ok := imports[name]; ok {
			if b.Remote != "" {
				return b.Module + "." + b.Remote
			}
			return b.Module + "." + name
		}
		// Top-level call inside the same module: let the parser's
		// within-file symbol map be the source of truth (the existing
		// qualified match pass will link it).
		return selfModule + "." + name
	case "attribute":
		obj := fn.ChildByFieldName("object")
		attr := fn.ChildByFieldName("attribute")
		if obj == nil || attr == nil {
			return ""
		}
		attrName := tsutil.Slice(content, attr)

		// self.method / cls.method inside a class
		if obj.Type() == "identifier" {
			base := tsutil.Slice(content, obj)
			if (base == "self" || base == "cls") && currentClass != "" {
				if methods, ok := classMethods[currentClass]; ok {
					if q, ok := methods[attrName]; ok {
						return q
					}
				}
			}
			// module.attribute via import: `foo.bar()` where foo was imported
			if b, ok := imports[base]; ok && b.Remote == "" {
				return b.Module + "." + attrName
			}
		}
		// Other obj shapes (chained attributes, subscripts, calls): we
		// can't follow without type inference. Leave unqualified.
		return ""
	}
	return ""
}

// --- helpers ---------------------------------------------------------------

// moduleName matches what internal/parser/python uses so the qualified
// names we produce line up with the parser's symbol qualified names.
func moduleName(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// shortModule takes a dotted import path like "a.b.c" and returns "c" —
// matches the parser's moduleName (file-basename-less-extension) for
// cross-module symbol linking. For relative-package imports the last
// segment is what our symbol qualifier uses.
func shortModule(dotted string) string {
	s := strings.TrimSpace(dotted)
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func isSameNode(a, b *sitter.Node) bool {
	if a == nil || b == nil {
		return false
	}
	return a.StartByte() == b.StartByte() && a.EndByte() == b.EndByte() && a.Type() == b.Type()
}

// parserCallName mirrors internal/parser/python.callName (for matching
// resolver visits against parser refs).
func parserCallName(content []byte, fn *sitter.Node) string {
	switch fn.Type() {
	case "identifier":
		return tsutil.Slice(content, fn)
	case "attribute":
		obj := fn.ChildByFieldName("object")
		attr := fn.ChildByFieldName("attribute")
		lhs := parserCallName(content, obj)
		if attr == nil {
			return lhs
		}
		rhs := tsutil.Slice(content, attr)
		if lhs == "" {
			return rhs
		}
		return lhs + "." + rhs
	}
	return ""
}

func lastSegment(dotted string) string {
	for i := len(dotted) - 1; i >= 0; i-- {
		if dotted[i] == '.' {
			return dotted[i+1:]
		}
	}
	return dotted
}

// readFile is a tiny wrapper so tests can inject a fake filesystem later
// without touching the main path. Intentionally thin.
var readFile = func(path string) ([]byte, error) {
	return osReadFile(path)
}
