package golang

import (
	"context"
	"crypto/sha256"
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/jdwiederstein/mycelium/internal/parser"
)

// Parser implements parser.Parser for Go source using the standard go/ast.
// Chosen over tree-sitter for v0.1: no cgo, more accurate for Go, smaller dep.
// A tree-sitter-backed implementation can replace this behind the same interface
// once we unify with TS/Python parsers.
type Parser struct{}

func New() *Parser { return &Parser{} }

func (p *Parser) Language() string { return "go" }

func (p *Parser) Supports(path string) bool {
	return filepath.Ext(path) == ".go"
}

func (p *Parser) Parse(_ context.Context, path string, content []byte) (parser.ParseResult, error) {
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, path, content, goparser.ParseComments)
	if err != nil {
		return parser.ParseResult{}, fmt.Errorf("parse %s: %w", path, err)
	}

	pkg := file.Name.Name
	result := parser.ParseResult{
		Path:        path,
		Language:    "go",
		ContentHash: sum(content),
	}

	// Imports map alias -> import path, for resolving selector references to imports.
	// v0.1: we only record imports as references of kind=import; full selector
	// resolution lands later.
	for _, imp := range file.Imports {
		ipath := strings.Trim(imp.Path.Value, `"`)
		line := fset.Position(imp.Pos()).Line
		col := fset.Position(imp.Pos()).Column
		result.References = append(result.References, parser.Reference{
			DstName: ipath,
			Kind:    parser.RefImport,
			Line:    line,
			Col:     col,
		})
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			sym := extractFunc(fset, file, pkg, content, d)
			result.Symbols = append(result.Symbols, sym)
			result.References = append(result.References, extractCalls(fset, sym.Qualified, d.Body)...)
		case *ast.GenDecl:
			result.Symbols = append(result.Symbols, extractGenDecl(fset, file, pkg, content, d)...)
		}
	}

	result.ParseHash = hashParseResult(result)
	return result, nil
}

func extractFunc(fset *token.FileSet, file *ast.File, pkg string, content []byte, d *ast.FuncDecl) parser.Symbol {
	name := d.Name.Name
	start := fset.Position(d.Pos())
	end := fset.Position(d.End())

	kind := parser.KindFunction
	qualified := pkg + "." + name
	parent := ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = parser.KindMethod
		parent = receiverTypeName(d.Recv.List[0].Type)
		if parent != "" {
			qualified = pkg + "." + parent + "." + name
		}
	}

	sig := funcSignature(d, content)
	doc := commentText(d.Doc)

	return parser.Symbol{
		Name:       name,
		Qualified:  qualified,
		Kind:       kind,
		StartLine:  start.Line,
		StartCol:   start.Column,
		EndLine:    end.Line,
		EndCol:     end.Column,
		Signature:  sig,
		Docstring:  doc,
		Visibility: goVisibility(name),
		ParentName: parent,
		Hash:       sum([]byte(sig + "\x00" + sliceBody(content, d.Pos(), d.End()))),
	}
}

func extractGenDecl(fset *token.FileSet, file *ast.File, pkg string, content []byte, d *ast.GenDecl) []parser.Symbol {
	var out []parser.Symbol
	groupDoc := commentText(d.Doc)
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			kind := parser.KindType
			if _, ok := s.Type.(*ast.InterfaceType); ok {
				kind = parser.KindInterface
			}
			start := fset.Position(s.Pos())
			end := fset.Position(s.End())
			name := s.Name.Name
			sig := "type " + name + " " + typeExprText(s.Type, content)
			doc := commentText(s.Doc)
			if doc == "" {
				doc = groupDoc
			}
			out = append(out, parser.Symbol{
				Name:       name,
				Qualified:  pkg + "." + name,
				Kind:       kind,
				StartLine:  start.Line,
				StartCol:   start.Column,
				EndLine:    end.Line,
				EndCol:     end.Column,
				Signature:  sig,
				Docstring:  doc,
				Visibility: goVisibility(name),
				Hash:       sum([]byte(sig + "\x00" + sliceBody(content, s.Pos(), s.End()))),
			})
		case *ast.ValueSpec:
			var kind parser.Kind
			switch d.Tok {
			case token.VAR:
				kind = parser.KindVar
			case token.CONST:
				kind = parser.KindConst
			default:
				continue
			}
			start := fset.Position(s.Pos())
			end := fset.Position(s.End())
			typeText := ""
			if s.Type != nil {
				typeText = " " + typeExprText(s.Type, content)
			}
			doc := commentText(s.Doc)
			if doc == "" {
				doc = groupDoc
			}
			for _, nm := range s.Names {
				sig := string(d.Tok) + " " + nm.Name + typeText
				out = append(out, parser.Symbol{
					Name:       nm.Name,
					Qualified:  pkg + "." + nm.Name,
					Kind:       kind,
					StartLine:  start.Line,
					StartCol:   start.Column,
					EndLine:    end.Line,
					EndCol:     end.Column,
					Signature:  sig,
					Docstring:  doc,
					Visibility: goVisibility(nm.Name),
					Hash:       sum([]byte(sig + "\x00" + sliceBody(content, s.Pos(), s.End()))),
				})
			}
		}
	}
	return out
}

// extractCalls walks a function body collecting call-site references.
// v0.1: we record the *textual* target (e.g. "foo", "pkg.Bar", "x.Method") as
// dst_name. Cross-file resolution happens later via index/refs resolution pass.
func extractCalls(fset *token.FileSet, srcQualified string, body *ast.BlockStmt) []parser.Reference {
	if body == nil {
		return nil
	}
	var refs []parser.Reference
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callTargetName(call.Fun)
		if name == "" {
			return true
		}
		pos := fset.Position(call.Pos())
		refs = append(refs, parser.Reference{
			SrcSymbolQualified: srcQualified,
			DstName:            name,
			Kind:               parser.RefCall,
			Line:               pos.Line,
			Col:                pos.Column,
		})
		return true
	})
	return refs
}

func callTargetName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		base := callTargetName(x.X)
		if base == "" {
			return x.Sel.Name
		}
		return base + "." + x.Sel.Name
	}
	return ""
}

func receiverTypeName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return receiverTypeName(x.X)
	case *ast.IndexExpr:
		return receiverTypeName(x.X)
	case *ast.IndexListExpr:
		return receiverTypeName(x.X)
	}
	return ""
}

func funcSignature(d *ast.FuncDecl, content []byte) string {
	// Slice from the "func" keyword through the closing paren of the result list.
	start := d.Pos()
	end := d.Type.End()
	if d.Body != nil {
		end = d.Body.Lbrace
	}
	return strings.TrimSpace(sliceBody(content, start, end))
}

func typeExprText(e ast.Expr, content []byte) string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(sliceBody(content, e.Pos(), e.End()))
}

func sliceBody(content []byte, start, end token.Pos) string {
	if start == token.NoPos || end == token.NoPos {
		return ""
	}
	s := int(start) - 1
	e := int(end) - 1
	if s < 0 {
		s = 0
	}
	if e > len(content) {
		e = len(content)
	}
	if s >= e {
		return ""
	}
	return string(content[s:e])
}

func commentText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return strings.TrimSpace(cg.Text())
}

func goVisibility(name string) parser.Visibility {
	if name == "" {
		return parser.VisPackage
	}
	if unicode.IsUpper([]rune(name)[0]) {
		return parser.VisPublic
	}
	return parser.VisPrivate
}

func sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func hashParseResult(r parser.ParseResult) []byte {
	h := sha256.New()
	for _, s := range r.Symbols {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", s.Qualified, s.Kind, s.Signature)
	}
	for _, ref := range r.References {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", ref.SrcSymbolQualified, ref.DstName, ref.Kind)
	}
	out := h.Sum(nil)
	return out
}
