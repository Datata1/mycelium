package typescript

import (
	"context"
	"os"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/tsutil"
)

// EmitInheritance appends RefInherit edges for `class X extends B`,
// `class X implements I` and `interface A extends B` — subclass/impl ->
// base/interface, mirroring the Go resolver's concrete -> interface
// direction. TS inheritance is syntactic, so no type checker is needed;
// heritage targets are qualified through the same import table
// ResolveFile uses. Mixin factories (`extends Mixin(Base)`) and other
// non-name expressions are skipped.
func (r *Resolver) EmitInheritance(absPath string, pr *parser.ParseResult) int {
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
	emit := func(srcQualified string, target *sitter.Node) {
		dst := heritageName(content, target, imports, module)
		if dst == "" {
			return
		}
		sl, sc, _, _ := tsutil.Position(target)
		pr.References = append(pr.References, parser.Reference{
			SrcSymbolQualified: srcQualified,
			DstName:            dst,
			Kind:               parser.RefInherit,
			Line:               sl,
			Col:                sc,
			ResolverVersion:    ResolverVersion,
		})
		added++
	}

	tsutil.Walk(tree.RootNode(), func(n *sitter.Node) bool {
		switch n.Type() {
		case "class_declaration", "abstract_class_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return true
			}
			src := module + "." + tsutil.Slice(content, nameNode)
			if _, ok := parserSyms[src]; !ok {
				return true
			}
			for i := uint32(0); i < n.NamedChildCount(); i++ {
				h := n.NamedChild(int(i))
				if h.Type() != "class_heritage" {
					continue
				}
				for j := uint32(0); j < h.NamedChildCount(); j++ {
					clause := h.NamedChild(int(j))
					// extends_clause (one expression) and
					// implements_clause (one or more type names).
					for k := uint32(0); k < clause.NamedChildCount(); k++ {
						emit(src, clause.NamedChild(int(k)))
					}
				}
			}
		case "interface_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return true
			}
			src := module + "." + tsutil.Slice(content, nameNode)
			if _, ok := parserSyms[src]; !ok {
				return true
			}
			for i := uint32(0); i < n.NamedChildCount(); i++ {
				clause := n.NamedChild(int(i))
				if clause.Type() != "extends_type_clause" {
					continue
				}
				for k := uint32(0); k < clause.NamedChildCount(); k++ {
					emit(src, clause.NamedChild(int(k)))
				}
			}
		}
		return true
	})
	return added
}

// heritageName resolves one heritage target node to a qualified name.
// Returns "" for shapes that aren't plain type names (mixin calls,
// computed expressions).
func heritageName(content []byte, n *sitter.Node, imports map[string]importBinding, selfModule string) string {
	switch n.Type() {
	case "identifier", "type_identifier":
		name := tsutil.Slice(content, n)
		if b, ok := imports[name]; ok {
			if b.Remote != "" {
				return b.Module + "." + b.Remote
			}
			return b.Module + "." + name
		}
		return selfModule + "." + name
	case "member_expression", "nested_type_identifier":
		// ns.Base — resolve the namespace through the import table when
		// possible, otherwise keep the dotted text (textual fallback).
		var objNode, propNode *sitter.Node
		if n.Type() == "member_expression" {
			objNode = n.ChildByFieldName("object")
			propNode = n.ChildByFieldName("property")
		} else {
			objNode = n.NamedChild(0)
			propNode = n.NamedChild(1)
		}
		if objNode == nil || propNode == nil || objNode.Type() != "identifier" {
			return ""
		}
		obj := tsutil.Slice(content, objNode)
		prop := tsutil.Slice(content, propNode)
		if b, ok := imports[obj]; ok && b.Namespace {
			return b.Module + "." + prop
		}
		return obj + "." + prop
	case "generic_type":
		// Base<T> — peel the type arguments.
		if inner := n.NamedChild(0); inner != nil {
			return heritageName(content, inner, imports, selfModule)
		}
	}
	return ""
}
