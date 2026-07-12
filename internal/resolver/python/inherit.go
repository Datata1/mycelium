package python

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/tsutil"
)

// EmitInheritance appends RefInherit edges (subclass -> base) for every
// class definition with superclasses. Python inheritance is syntactic,
// so no type checker is needed — bases are qualified through the same
// import table ResolveFile uses. Non-name bases (Generic[T] subscripts,
// metaclass= keyword arguments, computed expressions) are skipped.
func (r *Resolver) EmitInheritance(absPath string, pr *parser.ParseResult) int {
	r.once.Do(r.init)

	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(r.lang)
	content, err := readFile(absPath)
	if err != nil {
		return 0
	}
	tree, err := p.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return 0
	}
	defer tree.Close()

	module := moduleName(absPath)
	imports := buildImportTable(tree.RootNode(), content)

	// Only emit edges whose source matches a parser-produced symbol —
	// same guard as the Go emitter, avoids orphan refs.
	parserSyms := map[string]struct{}{}
	for _, s := range pr.Symbols {
		parserSyms[s.Qualified] = struct{}{}
	}

	added := 0
	tsutil.Walk(tree.RootNode(), func(n *sitter.Node) bool {
		if n.Type() != "class_definition" {
			return true
		}
		args := n.ChildByFieldName("superclasses")
		if args == nil {
			return true
		}
		src := qualifiedClassName(n, content, module)
		if _, ok := parserSyms[src]; !ok {
			return true
		}
		for i := uint32(0); i < args.NamedChildCount(); i++ {
			b := args.NamedChild(int(i))
			dst := baseName(content, b, imports, module)
			if dst == "" {
				continue
			}
			sl, sc, _, _ := tsutil.Position(b)
			pr.References = append(pr.References, parser.Reference{
				SrcSymbolQualified: src,
				DstName:            dst,
				Kind:               parser.RefInherit,
				Line:               sl,
				Col:                sc,
				ResolverVersion:    ResolverVersion,
			})
			added++
		}
		return true
	})
	return added
}

// qualifiedClassName renders module.Outer.Inner for (possibly nested)
// class definitions, matching the parser's symbol qualification.
func qualifiedClassName(n *sitter.Node, content []byte, module string) string {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return ""
	}
	parts := []string{tsutil.Slice(content, nameNode)}
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_definition" {
			if nm := p.ChildByFieldName("name"); nm != nil {
				parts = append([]string{tsutil.Slice(content, nm)}, parts...)
			}
		}
	}
	return module + "." + strings.Join(parts, ".")
}

// baseName resolves one superclass node to a qualified name. Returns ""
// for shapes that aren't plain names.
func baseName(content []byte, n *sitter.Node, imports map[string]importBinding, selfModule string) string {
	switch n.Type() {
	case "identifier":
		name := tsutil.Slice(content, n)
		if b, ok := imports[name]; ok {
			if b.Remote != "" {
				return b.Module + "." + b.Remote
			}
			return b.Module + "." + name
		}
		return selfModule + "." + name
	case "attribute":
		obj := n.ChildByFieldName("object")
		attr := n.ChildByFieldName("attribute")
		if obj == nil || attr == nil || obj.Type() != "identifier" {
			return ""
		}
		base := tsutil.Slice(content, obj)
		attrName := tsutil.Slice(content, attr)
		if b, ok := imports[base]; ok && b.Remote == "" {
			return b.Module + "." + attrName
		}
		return base + "." + attrName
	}
	return ""
}
