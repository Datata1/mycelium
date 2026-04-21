package typescript

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/tsutil"
)

// Parser extracts symbols and refs from TypeScript / TSX via tree-sitter.
// A single Parser is safe for concurrent Parse calls; the underlying
// *sitter.Parser is created per call (tree-sitter parsers are not
// thread-safe).
type Parser struct {
	once     sync.Once
	langTS   *sitter.Language
	langTSX  *sitter.Language
}

func New() *Parser { return &Parser{} }

func (p *Parser) Language() string { return "typescript" }

func (p *Parser) Supports(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".ts" || ext == ".tsx" || ext == ".mts" || ext == ".cts"
}

func (p *Parser) init() {
	p.langTS = typescript.GetLanguage()
	p.langTSX = tsx.GetLanguage()
}

func (p *Parser) Parse(ctx context.Context, path string, content []byte) (parser.ParseResult, error) {
	p.once.Do(p.init)

	ts := sitter.NewParser()
	defer ts.Close()
	if strings.HasSuffix(path, ".tsx") {
		ts.SetLanguage(p.langTSX)
	} else {
		ts.SetLanguage(p.langTS)
	}

	tree, err := ts.ParseCtx(ctx, nil, content)
	if err != nil {
		return parser.ParseResult{}, err
	}
	defer tree.Close()

	pkg := moduleName(path)
	ex := &extractor{
		content: content,
		pkg:     pkg,
		commentTypes: map[string]bool{
			"comment": true,
		},
	}
	ex.walkTop(tree.RootNode())

	return parser.ParseResult{
		Path:        path,
		Language:    "typescript",
		Symbols:     ex.symbols,
		References:  ex.refs,
		ContentHash: tsutil.ContentHash(content),
		ParseHash:   tsutil.ParseHash(ex.symbols, ex.refs),
	}, nil
}

// moduleName derives a package-ish prefix for qualified names. Using the
// file basename (sans extension) is conventional for TS modules and gives
// reasonable dedup without a full module resolver.
func moduleName(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

type extractor struct {
	content      []byte
	pkg          string
	commentTypes map[string]bool
	symbols      []parser.Symbol
	refs         []parser.Reference
}

func (ex *extractor) walkTop(root *sitter.Node) {
	// Top-level: iterate named children of the program/source_file. Export
	// wrappers are transparent.
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(int(i))
		ex.walkTopChild(root, child)
	}
}

func (ex *extractor) walkTopChild(parent, n *sitter.Node) {
	switch n.Type() {
	case "export_statement":
		// Descend into the export target. e.g. `export function foo() {}`
		for i := uint32(0); i < n.NamedChildCount(); i++ {
			ex.walkTopChild(n, n.NamedChild(int(i)))
		}
	case "function_declaration", "generator_function_declaration":
		ex.emitFunction(parent, n, "", parser.KindFunction)
	case "class_declaration":
		ex.emitClass(parent, n)
	case "interface_declaration":
		ex.emitSimpleNamed(parent, n, parser.KindInterface)
	case "type_alias_declaration":
		ex.emitSimpleNamed(parent, n, parser.KindType)
	case "enum_declaration":
		ex.emitSimpleNamed(parent, n, parser.KindType)
	case "lexical_declaration", "variable_statement":
		ex.emitTopLevelBindings(parent, n)
	case "import_statement":
		ex.emitImport(n)
	}
}

func (ex *extractor) emitFunction(parent, n *sitter.Node, ownerName string, kind parser.Kind) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := tsutil.Slice(ex.content, nameNode)
	qualified := ex.pkg + "." + name
	if ownerName != "" {
		qualified = ex.pkg + "." + ownerName + "." + name
	}
	sig := signatureUpToBody(ex.content, n)
	body := tsutil.Slice(ex.content, n)
	sl, sc, el, ec := tsutil.Position(n)
	vis := parser.VisPublic // TS top-level functions are public by default; class members handled below
	sym := parser.Symbol{
		Name:       name,
		Qualified:  qualified,
		Kind:       kind,
		StartLine:  sl, StartCol: sc,
		EndLine: el, EndCol: ec,
		Signature:  sig,
		Docstring:  tsutil.PrecedingComments(ex.content, parent, n, ex.commentTypes),
		Visibility: vis,
		ParentName: ownerName,
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
	qualified := ex.pkg + "." + name
	sl, sc, el, ec := tsutil.Position(n)
	body := tsutil.Slice(ex.content, n)
	sig := "class " + name
	ex.symbols = append(ex.symbols, parser.Symbol{
		Name: name, Qualified: qualified, Kind: parser.KindClass,
		StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec,
		Signature:  sig,
		Docstring:  tsutil.PrecedingComments(ex.content, parent, n, ex.commentTypes),
		Visibility: parser.VisPublic,
		Hash:       tsutil.SymbolHash(sig, body),
	})

	// Walk class body for methods.
	classBody := n.ChildByFieldName("body")
	if classBody == nil {
		return
	}
	for i := uint32(0); i < classBody.NamedChildCount(); i++ {
		m := classBody.NamedChild(int(i))
		switch m.Type() {
		case "method_definition", "method_signature":
			ex.emitMethod(classBody, m, name)
		case "public_field_definition":
			// Field with initializer — emit as a variable binding inside the class.
			ex.emitField(classBody, m, name)
		}
	}
}

func (ex *extractor) emitMethod(parent, n *sitter.Node, owner string) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := tsutil.Slice(ex.content, nameNode)
	qualified := ex.pkg + "." + owner + "." + name
	sig := signatureUpToBody(ex.content, n)
	body := tsutil.Slice(ex.content, n)
	sl, sc, el, ec := tsutil.Position(n)
	ex.symbols = append(ex.symbols, parser.Symbol{
		Name: name, Qualified: qualified, Kind: parser.KindMethod,
		StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec,
		Signature:  sig,
		Docstring:  tsutil.PrecedingComments(ex.content, parent, n, ex.commentTypes),
		Visibility: visibilityFromNode(ex.content, n),
		ParentName: owner,
		Hash:       tsutil.SymbolHash(sig, body),
	})
	ex.extractCalls(qualified, n)
}

func (ex *extractor) emitField(parent, n *sitter.Node, owner string) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := tsutil.Slice(ex.content, nameNode)
	sl, sc, el, ec := tsutil.Position(n)
	sig := tsutil.Slice(ex.content, n)
	ex.symbols = append(ex.symbols, parser.Symbol{
		Name: name, Qualified: ex.pkg + "." + owner + "." + name,
		Kind: parser.KindVar,
		StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec,
		Signature:  truncateLine(sig, 160),
		Visibility: visibilityFromNode(ex.content, n),
		ParentName: owner,
		Hash:       tsutil.SymbolHash(sig, sig),
	})
}

func (ex *extractor) emitSimpleNamed(parent, n *sitter.Node, kind parser.Kind) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := tsutil.Slice(ex.content, nameNode)
	qualified := ex.pkg + "." + name
	sig := tsutil.Slice(ex.content, n)
	sl, sc, el, ec := tsutil.Position(n)
	ex.symbols = append(ex.symbols, parser.Symbol{
		Name: name, Qualified: qualified, Kind: kind,
		StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec,
		Signature:  truncateLine(sig, 200),
		Docstring:  tsutil.PrecedingComments(ex.content, parent, n, ex.commentTypes),
		Visibility: parser.VisPublic,
		Hash:       tsutil.SymbolHash(sig, sig),
	})
}

func (ex *extractor) emitTopLevelBindings(parent, n *sitter.Node) {
	// lexical_declaration or variable_statement -> one or more variable_declarators.
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		d := n.NamedChild(int(i))
		if d.Type() != "variable_declarator" {
			continue
		}
		nameNode := d.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := tsutil.Slice(ex.content, nameNode)
		// Only handle simple identifier bindings for v0.3; destructuring is skipped.
		if nameNode.Type() != "identifier" {
			continue
		}
		sig := tsutil.Slice(ex.content, n)
		sl, sc, el, ec := tsutil.Position(d)
		ex.symbols = append(ex.symbols, parser.Symbol{
			Name:      name,
			Qualified: ex.pkg + "." + name,
			Kind:      parser.KindVar,
			StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec,
			Signature:  truncateLine(sig, 160),
			Docstring:  tsutil.PrecedingComments(ex.content, parent, n, ex.commentTypes),
			Visibility: parser.VisPublic,
			Hash:       tsutil.SymbolHash(sig, sig),
		})
	}
}

func (ex *extractor) emitImport(n *sitter.Node) {
	src := n.ChildByFieldName("source")
	if src == nil {
		return
	}
	raw := tsutil.Slice(ex.content, src)
	raw = strings.Trim(raw, "\"'`")
	sl, sc, _, _ := tsutil.Position(n)
	ex.refs = append(ex.refs, parser.Reference{
		DstName: raw,
		Kind:    parser.RefImport,
		Line:    sl,
		Col:     sc,
	})
}

func (ex *extractor) extractCalls(srcQualified string, scope *sitter.Node) {
	tsutil.Walk(scope, func(n *sitter.Node) bool {
		if n == scope {
			return true
		}
		// Don't descend into nested function bodies — those get their own
		// extraction via the top-level walk (for methods on the class).
		// But do descend into arrow functions inside the enclosing scope so
		// their calls are attributed to the outer symbol.
		if n.Type() == "call_expression" {
			fn := n.ChildByFieldName("function")
			name := callName(ex.content, fn)
			if name != "" {
				sl, sc, _, _ := tsutil.Position(n)
				ex.refs = append(ex.refs, parser.Reference{
					SrcSymbolQualified: srcQualified,
					DstName:            name,
					Kind:               parser.RefCall,
					Line:               sl,
					Col:                sc,
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
	case "member_expression":
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		lhs := callName(content, obj)
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

func signatureUpToBody(content []byte, n *sitter.Node) string {
	body := n.ChildByFieldName("body")
	if body == nil {
		// No body (e.g. method signature in an interface); take the whole thing.
		return strings.TrimSpace(truncateLine(tsutil.Slice(content, n), 200))
	}
	start := n.StartByte()
	end := body.StartByte()
	if end <= start || int(end) > len(content) {
		return ""
	}
	return strings.TrimSpace(string(content[start:end]))
}

func visibilityFromNode(content []byte, n *sitter.Node) parser.Visibility {
	// Access modifiers live as unnamed children (public/private/protected keywords).
	for i := uint32(0); i < n.ChildCount(); i++ {
		c := n.Child(int(i))
		switch c.Type() {
		case "accessibility_modifier":
			text := strings.TrimSpace(tsutil.Slice(content, c))
			switch text {
			case "private":
				return parser.VisPrivate
			case "protected":
				return parser.VisPackage
			}
		}
	}
	// Leading underscore is a widespread "private" convention in untyped JS.
	if nm := n.ChildByFieldName("name"); nm != nil {
		if s := tsutil.Slice(content, nm); strings.HasPrefix(s, "_") {
			return parser.VisPrivate
		}
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
