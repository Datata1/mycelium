package parser

import "context"

// Kind classifies a symbol definition.
type Kind string

const (
	KindFunction  Kind = "function"
	KindMethod    Kind = "method"
	KindType      Kind = "type"
	KindInterface Kind = "interface"
	KindClass     Kind = "class"
	KindVar       Kind = "var"
	KindConst     Kind = "const"
)

// RefKind classifies a reference between symbols.
type RefKind string

const (
	RefCall    RefKind = "call"
	RefImport  RefKind = "import"
	RefTypeRef RefKind = "type_ref"
	RefInherit RefKind = "inherit"
)

// Visibility captures public/private/package scope. Language-specific.
type Visibility string

const (
	VisPublic  Visibility = "public"
	VisPrivate Visibility = "private"
	VisPackage Visibility = "package"
)

// Symbol is a language-agnostic definition record emitted by a parser.
// Parsers do not populate database IDs; those are assigned at write time.
type Symbol struct {
	Name       string
	Qualified  string
	Kind       Kind
	StartLine  int
	StartCol   int
	EndLine    int
	EndCol     int
	Signature  string
	Docstring  string
	Visibility Visibility
	ParentName string
	Hash       []byte
}

// Reference is a use-site pointing at a (possibly unresolved) target name.
//
// ResolverVersion records which resolver produced the final DstName:
//   0 = textual only (parser's raw output, suitable for unique-short-name matching)
//   1 = go-types resolver (v1.2), authoritative shortpkg.Receiver.Method form
//
// Downstream (internal/index) uses this to decide whether to trust the ref
// for ambiguous names: a version=1 ref never falls back to the textual
// unique-short-name pass.
type Reference struct {
	SrcSymbolQualified string
	DstName            string
	Kind               RefKind
	Line               int
	Col                int
	ResolverVersion    int
}

// ParseResult bundles everything a parser extracts from a single file.
type ParseResult struct {
	Path       string
	Language   string
	Symbols    []Symbol
	References []Reference
	ContentHash []byte
	ParseHash   []byte
}

// Parser is the interface implemented by every language backend.
// Implementations must be safe to call concurrently.
type Parser interface {
	Language() string
	Supports(path string) bool
	Parse(ctx context.Context, path string, content []byte) (ParseResult, error)
}
