// Integration test for instantiation references. Composite literals
// (Go) and constructor calls (TS, covered in get_refs_test.go) are often
// the only inbound edge a type has; get_references must surface them.
package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
	goresolver "github.com/datata1/mycelium/internal/resolver/golang"
)

func TestIntegration_GetReferences_CompositeLiteral(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()

	writeFile(t, dst, "go.mod", "module example.com/lit\n\ngo 1.22\n")
	writeFile(t, dst, "widget.go", `package lit

type Widget struct {
	Name string
}
`)
	writeFile(t, dst, "build.go", `package lit

func build() *Widget {
	return &Widget{Name: "w"}
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	t.Cleanup(func() { ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(golang.New())

	gr := goresolver.New(dst)
	if _, err := gr.Load(); err != nil {
		t.Fatalf("resolver load: %v", err)
	}
	if !gr.Ready() {
		t.Fatalf("go resolver not ready (load errors: %v)", gr.LoadErrors())
	}

	walker := repo.NewWalker(dst, []string{"**/*.go"}, nil, 0)
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   walker,
		Resolvers: map[string]pipeline.Resolver{
			"go": gr,
		},
	}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}

	r := query.NewReader(ix.DB())

	res, err := r.GetReferences(ctx, "Widget", "", 20, nil)
	if err != nil {
		t.Fatalf("GetReferences: %v", err)
	}
	var found, resolved bool
	for _, h := range res.Matches {
		if strings.Contains(h.SrcPath, "build.go") && h.Kind == "type_ref" {
			found = true
			resolved = h.Resolved
			break
		}
	}
	if !found {
		t.Fatalf("expected a type_ref hit from build.go for &Widget{...}; got %+v", res.Matches)
	}
	if !resolved {
		t.Errorf("expected the composite-literal ref to be resolved to lit.Widget")
	}
}
