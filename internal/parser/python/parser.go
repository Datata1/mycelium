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

// Parser extracts symbols and refs from Python via tree-sitter.
type Parser struct {
	once sync.Once
	lang *sitter.Language
}

func New() *Parser { return &Parser{} }

func (p *Parser) Language() string { return "python" }

func (p *Parser) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".py"
}

func (p *Parser) init() { p.lang = python.GetLanguage() }

func (p *Parser) Parse(ctx context.Context, path string, content []byte) (parser.ParseResult, error) {
	p.once.Do(p.init)

	ts := sitter.NewParser()
	defer ts.Close()
	ts.SetLanguage(p.lang)

	tree, err := ts.ParseCtx(ctx, nil, content)
	if err != nil {
		return parser.ParseResult{}, err
	}
	defer tree.Close()

	ex := &extractor{
		content: content,
		module:  moduleName(path),
		commentTypes: map[string]bool{
			"comment": true,
		},
	}
	ex.walkTop(tree.RootNode())

	return parser.ParseResult{
		Path:        path,
		Language:    "python",
		Symbols:     ex.symbols,
		References:  ex.refs,
		ContentHash: tsutil.ContentHash(content),
		ParseHash:   tsutil.ParseHash(ex.symbols, ex.refs),
	}, nil
}

func moduleName(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

type extractor struct {
	content      []byte
	module       string
	commentTypes map[string]bool
	symbols      []parser.Symbol
	refs         []parser.Reference
}

func (ex *extractor) walkTop(root *sitter.Node) {
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		ex.walkTopChild(root, root.NamedChild(int(i)))
	}
}

func (ex *extractor) walkTopChild(parent, n *sitter.Node) {
	switch n.Type() {
	case "decorated_definition":
		// The underlying definition is a named child, typically the last one.
		for i := uint32(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(int(i))
			switch c.Type() {
			case "function_definition", "class_definition":
				ex.walkTopChild(parent, c)
			}
		}
	case "function_definition":
		ex.emitFunction(parent, n, "", parser.KindFunction)
	case "class_definition":
		ex.emitClass(parent, n)
	case "import_statement", "import_from_statement":
		ex.emitImport(n)
	}
}

func (ex *extractor) emitFunction(parent, n *sitter.Node, owner string, kind parser.Kind) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := tsutil.Slice(ex.content, nameNode)
	qualified := ex.module + "." + name
	if owner != "" {
		qualified = ex.module + "." + owner + "." + name
	}
	sig := signatureUpToBody(ex.content, n)
	body := tsutil.Slice(ex.content, n)
	sl, sc, el, ec := tsutil.Position(n)
	sym := parser.Symbol{
		Name: name, Qualified: qualified, Kind: kind,
		StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec,
		Signature:  sig,
		Docstring:  pythonDocstring(ex.content, n, parent, ex.commentTypes),
		Visibility: pythonVisibility(name),
		ParentName: owner,
		Hash:       tsutil.SymbolHash(sig, body),
	}
	ex.symbols = append(ex.symbols, sym)
	ex.extractCalls(qualified, n)
}

func (ex *extractor) emitClass(parent, n *sitter.Node) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := tsutil.Slice(ex.content, nameNode)
	qualified := ex.module + "." + name
	sig := signatureUpToBody(ex.content, n)
	body := tsutil.Slice(ex.content, n)
	sl, sc, el, ec := tsutil.Position(n)
	ex.symbols = append(ex.symbols, parser.Symbol{
		Name: name, Qualified: qualified, Kind: parser.KindClass,
		StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec,
		Signature:  sig,
		Docstring:  pythonDocstring(ex.content, n, parent, ex.commentTypes),
		Visibility: pythonVisibility(name),
		Hash:       tsutil.SymbolHash(sig, body),
	})

	body2 := n.ChildByFieldName("body")
	if body2 == nil {
		return
	}
	// Class body: walk named children; function_definition -> method.
	for i := uint32(0); i < body2.NamedChildCount(); i++ {
		c := body2.NamedChild(int(i))
		switch c.Type() {
		case "decorated_definition":
			for j := uint32(0); j < c.NamedChildCount(); j++ {
				cc := c.NamedChild(int(j))
				if cc.Type() == "function_definition" {
					ex.emitFunction(body2, cc, name, parser.KindMethod)
				}
			}
		case "function_definition":
			ex.emitFunction(body2, c, name, parser.KindMethod)
		}
	}
}

func (ex *extractor) emitImport(n *sitter.Node) {
	// import_statement: "import" dotted_name (',' dotted_name)*
	// import_from_statement: "from" dotted_name "import" ...
	sl, sc, _, _ := tsutil.Position(n)
	switch n.Type() {
	case "import_statement":
		for i := uint32(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(int(i))
			if c.Type() == "dotted_name" || c.Type() == "aliased_import" {
				name := tsutil.Slice(ex.content, c)
				ex.refs = append(ex.refs, parser.Reference{
					DstName: strings.TrimSpace(name),
					Kind:    parser.RefImport,
					Line:    sl, Col: sc,
				})
			}
		}
	case "import_from_statement":
		if src := n.ChildByFieldName("module_name"); src != nil {
			ex.refs = append(ex.refs, parser.Reference{
				DstName: strings.TrimSpace(tsutil.Slice(ex.content, src)),
				Kind:    parser.RefImport,
				Line:    sl, Col: sc,
			})
		}
	}
}

func (ex *extractor) extractCalls(srcQualified string, scope *sitter.Node) {
	tsutil.Walk(scope, func(n *sitter.Node) bool {
		if n == scope {
			return true
		}
		if n.Type() == "call" {
			fn := n.ChildByFieldName("function")
			name := callName(ex.content, fn)
			if name != "" {
				sl, sc, _, _ := tsutil.Position(n)
				ex.refs = append(ex.refs, parser.Reference{
					SrcSymbolQualified: srcQualified,
					DstName:            name,
					Kind:               parser.RefCall,
					Line:               sl, Col: sc,
				})
			}
		}
		return true
	})
}

func callName(content []byte, fn *sitter.Node) string {
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier":
		return tsutil.Slice(content, fn)
	case "attribute":
		obj := fn.ChildByFieldName("object")
		attr := fn.ChildByFieldName("attribute")
		lhs := callName(content, obj)
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

// pythonDocstring returns the first statement of a block if it is a plain
// string literal (PEP 257). Falls back to preceding # comments if absent.
func pythonDocstring(content []byte, n, parent *sitter.Node, commentTypes map[string]bool) string {
	body := n.ChildByFieldName("body")
	if body != nil && body.NamedChildCount() > 0 {
		first := body.NamedChild(0)
		if first.Type() == "expression_statement" && first.NamedChildCount() > 0 {
			inner := first.NamedChild(0)
			if inner.Type() == "string" {
				s := tsutil.Slice(content, inner)
				return strings.TrimSpace(trimQuotes(s))
			}
		}
	}
	return tsutil.PrecedingComments(content, parent, n, commentTypes)
}

func trimQuotes(s string) string {
	// Handles triple- and single-quoted strings.
	for _, q := range []string{`"""`, `'''`, `"`, `'`} {
		if strings.HasPrefix(s, q) && strings.HasSuffix(s, q) && len(s) >= 2*len(q) {
			return s[len(q) : len(s)-len(q)]
		}
	}
	return s
}

func signatureUpToBody(content []byte, n *sitter.Node) string {
	body := n.ChildByFieldName("body")
	if body == nil {
		return strings.TrimSpace(truncateLine(tsutil.Slice(content, n), 200))
	}
	start := n.StartByte()
	end := body.StartByte()
	if end <= start || int(end) > len(content) {
		return ""
	}
	sig := string(content[start:end])
	return strings.TrimSpace(strings.TrimSuffix(sig, ":"))
}

// pythonVisibility follows the widely-used naming convention: a leading
// underscore marks private. Dunder names (__foo__) are treated as public.
func pythonVisibility(name string) parser.Visibility {
	if strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__") {
		return parser.VisPublic
	}
	if strings.HasPrefix(name, "_") {
		return parser.VisPrivate
	}
	return parser.VisPublic
}

func truncateLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
