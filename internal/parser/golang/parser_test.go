package golang

import (
	"bytes"
	"context"
	"testing"

	"github.com/datata1/mycelium/internal/parser"
)

type symWant struct {
	name   string
	kind   parser.Kind
	parent string
	vis    parser.Visibility
}

type refWant struct {
	src  string
	dst  string
	kind parser.RefKind
}

func findSymbol(t *testing.T, syms []parser.Symbol, name string) parser.Symbol {
	t.Helper()
	for _, s := range syms {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("symbol %q not found in %v", name, symbolNames(syms))
	return parser.Symbol{}
}

func findRef(t *testing.T, refs []parser.Reference, dst string, kind parser.RefKind) parser.Reference {
	t.Helper()
	for _, r := range refs {
		if r.DstName == dst && r.Kind == kind {
			return r
		}
	}
	t.Fatalf("reference dst=%q kind=%q not found in %+v", dst, kind, refs)
	return parser.Reference{}
}

func symbolNames(syms []parser.Symbol) []string {
	names := make([]string, len(syms))
	for i, s := range syms {
		names[i] = s.Name
	}
	return names
}

func TestParse(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantSyms []symWant
		wantRefs []refWant
	}{
		{
			name: "package-level functions",
			src: `package sample

func Exported() {}

func unexported() {}
`,
			wantSyms: []symWant{
				{name: "Exported", kind: parser.KindFunction, vis: parser.VisPublic},
				{name: "unexported", kind: parser.KindFunction, vis: parser.VisPrivate},
			},
		},
		{
			name: "method with receiver",
			src: `package sample

type Greeter struct{}

func (g *Greeter) Greet() string { return "hi" }
`,
			wantSyms: []symWant{
				{name: "Greeter", kind: parser.KindType, vis: parser.VisPublic},
				{name: "Greet", kind: parser.KindMethod, parent: "Greeter", vis: parser.VisPublic},
			},
		},
		{
			name: "struct and interface types",
			src: `package sample

type store struct {
	items []string
}

type Speaker interface {
	Speak() string
}
`,
			wantSyms: []symWant{
				{name: "store", kind: parser.KindType, vis: parser.VisPrivate},
				{name: "Speaker", kind: parser.KindInterface, vis: parser.VisPublic},
			},
		},
		{
			name: "const and var",
			src: `package sample

const MaxItems = 10

var counter int
`,
			wantSyms: []symWant{
				{name: "MaxItems", kind: parser.KindConst, vis: parser.VisPublic},
				{name: "counter", kind: parser.KindVar, vis: parser.VisPrivate},
			},
		},
		{
			name: "import and call references",
			src: `package sample

import "fmt"

func report(name string) {
	fmt.Println(name)
	local()
}

func local() {}
`,
			wantRefs: []refWant{
				{dst: "fmt", kind: parser.RefImport},
				{src: "sample.report", dst: "fmt.Println", kind: parser.RefCall},
				{src: "sample.report", dst: "local", kind: parser.RefCall},
			},
		},
	}

	p := New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(context.Background(), "sample.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if res.Language != "go" {
				t.Errorf("Language = %q, want %q", res.Language, "go")
			}
			for _, w := range tc.wantSyms {
				got := findSymbol(t, res.Symbols, w.name)
				if got.Kind != w.kind {
					t.Errorf("%s: Kind = %q, want %q", w.name, got.Kind, w.kind)
				}
				if got.ParentName != w.parent {
					t.Errorf("%s: ParentName = %q, want %q", w.name, got.ParentName, w.parent)
				}
				if got.Visibility != w.vis {
					t.Errorf("%s: Visibility = %q, want %q", w.name, got.Visibility, w.vis)
				}
			}
			for _, w := range tc.wantRefs {
				got := findRef(t, res.References, w.dst, w.kind)
				if got.SrcSymbolQualified != w.src {
					t.Errorf("ref %s: SrcSymbolQualified = %q, want %q", w.dst, got.SrcSymbolQualified, w.src)
				}
			}
		})
	}
}

func TestSupports(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"pkg/deep/file.go", true},
		{"script.py", false},
		{"app.ts", false},
		{"README.md", false},
		{"go", false},
	}
	p := New()
	for _, tc := range cases {
		if got := p.Supports(tc.path); got != tc.want {
			t.Errorf("Supports(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestHashStability(t *testing.T) {
	src := []byte(`package sample

func Do() { helper() }

func helper() {}
`)
	p := New()
	a, err := p.Parse(context.Background(), "sample.go", src)
	if err != nil {
		t.Fatalf("first Parse: %v", err)
	}
	b, err := p.Parse(context.Background(), "sample.go", src)
	if err != nil {
		t.Fatalf("second Parse: %v", err)
	}
	if len(a.ContentHash) == 0 {
		t.Error("ContentHash is empty")
	}
	if len(a.ParseHash) == 0 {
		t.Error("ParseHash is empty")
	}
	if !bytes.Equal(a.ContentHash, b.ContentHash) {
		t.Error("ContentHash differs across identical Parse calls")
	}
	if !bytes.Equal(a.ParseHash, b.ParseHash) {
		t.Error("ParseHash differs across identical Parse calls")
	}
}
