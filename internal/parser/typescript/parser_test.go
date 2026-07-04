package typescript

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
			name: "exported function",
			src: `export function createWidget(id: string): string {
  return id;
}
`,
			wantSyms: []symWant{
				{name: "createWidget", kind: parser.KindFunction, vis: parser.VisPublic},
			},
		},
		{
			name: "class with methods and visibility",
			src: `export class WidgetStore {
  add(id: string): void {
    render(id);
  }

  private reset(): void {}
}
`,
			wantSyms: []symWant{
				{name: "WidgetStore", kind: parser.KindClass, vis: parser.VisPublic},
				{name: "add", kind: parser.KindMethod, parent: "WidgetStore", vis: parser.VisPublic},
				{name: "reset", kind: parser.KindMethod, parent: "WidgetStore", vis: parser.VisPrivate},
			},
			wantRefs: []refWant{
				{src: "widget.WidgetStore.add", dst: "render", kind: parser.RefCall},
			},
		},
		{
			name: "interface and type alias",
			src: `export interface Widget {
  id: string;
}

export type WidgetId = string;
`,
			wantSyms: []symWant{
				{name: "Widget", kind: parser.KindInterface, vis: parser.VisPublic},
				{name: "WidgetId", kind: parser.KindType, vis: parser.VisPublic},
			},
		},
		{
			name: "imports",
			src: `import { render } from "./render";
import * as util from "node:util";
`,
			wantRefs: []refWant{
				{dst: "./render", kind: parser.RefImport},
				{dst: "node:util", kind: parser.RefImport},
			},
		},
		{
			name: "call references in a function",
			src: `function build(): void {
  helper();
  console.log("done");
}

function helper(): void {}
`,
			wantRefs: []refWant{
				{src: "widget.build", dst: "helper", kind: parser.RefCall},
				{src: "widget.build", dst: "console.log", kind: parser.RefCall},
			},
		},
		{
			name: "constructor calls via new",
			src: `import { Templater } from "./templater";
import * as pkg from "./pkg";

function build(): void {
  const t = new Templater(new pkg.Options(), 3);
  t.run();
}

class Local {}

function makeLocal(): Local {
  return new Local();
}
`,
			wantRefs: []refWant{
				{src: "widget.build", dst: "Templater", kind: parser.RefCall},
				{src: "widget.build", dst: "pkg.Options", kind: parser.RefCall},
				{src: "widget.makeLocal", dst: "Local", kind: parser.RefCall},
			},
		},
	}

	p := New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(context.Background(), "widget.ts", []byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if res.Language != "typescript" {
				t.Errorf("Language = %q, want %q", res.Language, "typescript")
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
		{"app.ts", true},
		{"component.tsx", true},
		{"mod.mts", true},
		{"legacy.cts", true},
		{"Widget.TS", true},
		{"main.go", false},
		{"script.py", false},
		{"index.js", false},
		{"ts", false},
	}
	p := New()
	for _, tc := range cases {
		if got := p.Supports(tc.path); got != tc.want {
			t.Errorf("Supports(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestHashStability(t *testing.T) {
	src := []byte(`export function doWork(): void {
  helper();
}

function helper(): void {}
`)
	p := New()
	a, err := p.Parse(context.Background(), "widget.ts", src)
	if err != nil {
		t.Fatalf("first Parse: %v", err)
	}
	b, err := p.Parse(context.Background(), "widget.ts", src)
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
