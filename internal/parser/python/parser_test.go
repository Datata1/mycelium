package python

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
			name: "module-level functions",
			src: `def make_config(path):
    return path

def _internal():
    pass
`,
			wantSyms: []symWant{
				{name: "make_config", kind: parser.KindFunction, vis: parser.VisPublic},
				{name: "_internal", kind: parser.KindFunction, vis: parser.VisPrivate},
			},
		},
		{
			name: "class with methods",
			src: `class Config:
    def __init__(self, path):
        self.path = path

    def reload(self):
        self._read()

    def _read(self):
        pass
`,
			wantSyms: []symWant{
				{name: "Config", kind: parser.KindClass, vis: parser.VisPublic},
				{name: "__init__", kind: parser.KindMethod, parent: "Config", vis: parser.VisPublic},
				{name: "reload", kind: parser.KindMethod, parent: "Config", vis: parser.VisPublic},
				{name: "_read", kind: parser.KindMethod, parent: "Config", vis: parser.VisPrivate},
			},
			wantRefs: []refWant{
				{src: "conf.Config.reload", dst: "self._read", kind: parser.RefCall},
			},
		},
		{
			name: "imports",
			src: `import os
import os.path
from collections import OrderedDict
`,
			wantRefs: []refWant{
				{dst: "os", kind: parser.RefImport},
				{dst: "os.path", kind: parser.RefImport},
				{dst: "collections", kind: parser.RefImport},
			},
		},
		{
			name: "call references",
			src: `def build():
    helper()
    os.getenv("HOME")

def helper():
    pass
`,
			wantRefs: []refWant{
				{src: "conf.build", dst: "helper", kind: parser.RefCall},
				{src: "conf.build", dst: "os.getenv", kind: parser.RefCall},
			},
		},
	}

	p := New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(context.Background(), "conf.py", []byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if res.Language != "python" {
				t.Errorf("Language = %q, want %q", res.Language, "python")
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
		{"script.py", true},
		{"pkg/module.PY", true},
		{"main.go", false},
		{"app.ts", false},
		{"notes.txt", false},
		{"py", false},
	}
	p := New()
	for _, tc := range cases {
		if got := p.Supports(tc.path); got != tc.want {
			t.Errorf("Supports(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestHashStability(t *testing.T) {
	src := []byte(`def do():
    helper()

def helper():
    pass
`)
	p := New()
	a, err := p.Parse(context.Background(), "conf.py", src)
	if err != nil {
		t.Fatalf("first Parse: %v", err)
	}
	b, err := p.Parse(context.Background(), "conf.py", src)
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
