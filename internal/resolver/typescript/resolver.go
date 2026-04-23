package typescript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/tsutil"
)

// ResolverVersion tags every call the TS resolver visits.
const ResolverVersion = 2

// Resolver is stateless per-file (same design as python.Resolver).
type Resolver struct {
	once    sync.Once
	langTS  *sitter.Language
	langTSX *sitter.Language
}

func New() *Resolver { return &Resolver{} }

func (r *Resolver) Ready() bool { return true }

func (r *Resolver) init() {
	r.langTS = typescript.GetLanguage()
	r.langTSX = tsx.GetLanguage()
}

// ResolveFile does an independent tree-sitter parse so it stays decoupled
// from the parser package. Cost: ~1ms/file for tree-sitter on average TS.
func (r *Resolver) ResolveFile(absPath string, pr *parser.ParseResult) (resolved, total int) {
	r.once.Do(r.init)

	p := sitter.NewParser()
	defer p.Close()
	if strings.HasSuffix(absPath, ".tsx") {
		p.SetLanguage(r.langTSX)
	} else {
		p.SetLanguage(r.langTS)
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return 0, 0
	}
	tree, err := p.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return 0, 0
	}
	defer tree.Close()

	module := moduleName(absPath)
	imports := buildImportTable(tree.RootNode(), content)
	classMethods := buildClassMethods(tree.RootNode(), content, module)

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

	tsutil.Walk(tree.RootNode(), func(n *sitter.Node) bool {
		if n.Type() != "call_expression" {
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
		pr.References[idx].ResolverVersion = ResolverVersion
		resolved++
		if q := resolveCall(content, fn, imports, classMethods, enclosingClass(n, content), module); q != "" {
			pr.References[idx].DstName = q
		}
		return true
	})
	return resolved, total
}

// --- import tables --------------------------------------------------------

// importBinding describes where a local name comes from.
// Namespace=true means the binding is `import * as x from "m"` — member
// accesses on the binding resolve to members of module m.
type importBinding struct {
	Module    string
	Remote    string // empty for default + namespace imports
	Namespace bool
}

// buildImportTable walks top-level import statements. Tree-sitter-typescript
// models them as `import_statement` with nested `import_clause`.
func buildImportTable(root *sitter.Node, content []byte) map[string]importBinding {
	out := map[string]importBinding{}
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(int(i))
		if n.Type() != "import_statement" {
			continue
		}
		source := n.ChildByFieldName("source")
		if source == nil {
			continue
		}
		mod := shortModuleFromSpec(strings.Trim(tsutil.Slice(content, source), `"'`+"`"))
		addImportClause(n, content, mod, out)
	}
	return out
}

func addImportClause(stmt *sitter.Node, content []byte, mod string, out map[string]importBinding) {
	// The children of import_statement carry the clauses. Look for
	// import_clause nodes (there can be mixed forms like `import Foo, { bar }`).
	for i := uint32(0); i < stmt.NamedChildCount(); i++ {
		c := stmt.NamedChild(int(i))
		switch c.Type() {
		case "import_clause":
			walkClause(c, content, mod, out)
		case "named_imports":
			walkNamedImports(c, content, mod, out)
		case "namespace_import":
			walkNamespaceImport(c, content, mod, out)
		case "identifier":
			// `import Foo from "..."` — default import appears as a bare
			// identifier child of the import_statement on some grammars.
			out[tsutil.Slice(content, c)] = importBinding{Module: mod}
		}
	}
}

func walkClause(clause *sitter.Node, content []byte, mod string, out map[string]importBinding) {
	for i := uint32(0); i < clause.NamedChildCount(); i++ {
		c := clause.NamedChild(int(i))
		switch c.Type() {
		case "identifier":
			// Default import binding.
			out[tsutil.Slice(content, c)] = importBinding{Module: mod}
		case "named_imports":
			walkNamedImports(c, content, mod, out)
		case "namespace_import":
			walkNamespaceImport(c, content, mod, out)
		}
	}
}

func walkNamedImports(n *sitter.Node, content []byte, mod string, out map[string]importBinding) {
	// { foo, bar as b, baz }
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		spec := n.NamedChild(int(i))
		if spec.Type() != "import_specifier" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		aliasNode := spec.ChildByFieldName("alias")
		if nameNode == nil {
			continue
		}
		remote := tsutil.Slice(content, nameNode)
		local := remote
		if aliasNode != nil {
			local = tsutil.Slice(content, aliasNode)
		}
		out[local] = importBinding{Module: mod, Remote: remote}
	}
}

func walkNamespaceImport(n *sitter.Node, content []byte, mod string, out map[string]importBinding) {
	// * as ns
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(int(i))
		if c.Type() == "identifier" {
			out[tsutil.Slice(content, c)] = importBinding{Module: mod, Namespace: true}
			return
		}
	}
}

// buildClassMethods returns {className: {methodName: qualified}} for every
// class_declaration in the file. Used to resolve this.method() calls.
func buildClassMethods(root *sitter.Node, content []byte, module string) map[string]map[string]string {
	out := map[string]map[string]string{}
	// Classes can appear at top level or wrapped in an export_statement.
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		for i := uint32(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(int(i))
			switch c.Type() {
			case "export_statement":
				walk(c)
			case "class_declaration":
				collectClass(c, content, module, out)
			}
		}
	}
	walk(root)
	return out
}

func collectClass(n *sitter.Node, content []byte, module string, out map[string]map[string]string) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := tsutil.Slice(content, nameNode)
	methods := map[string]string{}
	body := n.ChildByFieldName("body")
	if body == nil {
		out[className] = methods
		return
	}
	for i := uint32(0); i < body.NamedChildCount(); i++ {
		m := body.NamedChild(int(i))
		if m.Type() != "method_definition" && m.Type() != "method_signature" {
			continue
		}
		nm := m.ChildByFieldName("name")
		if nm == nil {
			continue
		}
		methodName := tsutil.Slice(content, nm)
		methods[methodName] = module + "." + className + "." + methodName
	}
	out[className] = methods
}

func enclosingClass(n *sitter.Node, content []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_declaration" {
			if nm := p.ChildByFieldName("name"); nm != nil {
				return tsutil.Slice(content, nm)
			}
		}
	}
	return ""
}

// --- call resolution -------------------------------------------------------

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
		return selfModule + "." + name
	case "member_expression":
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return ""
		}
		propName := tsutil.Slice(content, prop)

		// `this.method(...)` inside a class
		if obj.Type() == "this" && currentClass != "" {
			if methods, ok := classMethods[currentClass]; ok {
				if q, ok := methods[propName]; ok {
					return q
				}
			}
			return ""
		}

		// `ns.method(...)` where ns was `import * as ns`
		if obj.Type() == "identifier" {
			base := tsutil.Slice(content, obj)
			if b, ok := imports[base]; ok {
				if b.Namespace {
					return b.Module + "." + propName
				}
				// `Class.staticMethod()` via default/named import
				if b.Remote != "" {
					return b.Module + "." + b.Remote + "." + propName
				}
				return b.Module + "." + base + "." + propName
			}
		}
		// Anything else — chained calls, subscripts, arbitrary expressions —
		// needs type inference we intentionally don't do.
		return ""
	}
	return ""
}

// --- helpers --------------------------------------------------------------

func moduleName(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// shortModuleFromSpec turns an import specifier into the short module name
// the parser's moduleName produces for a file (basename minus extension).
// "./bar/auth" -> "auth"; "@scope/pkg" -> "pkg".
func shortModuleFromSpec(spec string) string {
	s := strings.TrimSpace(spec)
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

// parserCallName mirrors internal/parser/typescript.callName so we can
// match AST positions against parser-emitted refs for chained calls.
func parserCallName(content []byte, fn *sitter.Node) string {
	switch fn.Type() {
	case "identifier":
		return tsutil.Slice(content, fn)
	case "member_expression":
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		lhs := parserCallName(content, obj)
		if prop == nil {
			return lhs
		}
		rhs := tsutil.Slice(content, prop)
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
