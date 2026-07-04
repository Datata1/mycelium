// Integration test for get_references with class static-method calls (field-test
// regression). Reproduces the codesphere monorepo finding where
// get_references("CsEnv") returned null even though CsEnv.from() and
// CsEnv.empty() were called from spec.ts.
//
// Root cause: CsEnv.from() resolves to the *method* symbol ("env.CsEnv.from"),
// not the *class* symbol ("env.CsEnv"). symbolsByTarget only returns the class
// ID, so the first-pass query never matches the resolved ref.
package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/typescript"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
	tsresolver "github.com/datata1/mycelium/internal/resolver/typescript"
)

func TestIntegration_GetReferences_ClassStaticMethods(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, "testdata/fixtures/get-refs")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	defer ix.Close()

	reg := parser.NewRegistry()
	reg.Register(typescript.New())

	walker := repo.NewWalker(dst, []string{"src/**/*.ts"}, nil, 0)
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   walker,
		Resolvers: map[string]pipeline.Resolver{
			"typescript": tsresolver.New(),
		},
	}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}

	reader := query.NewReader(ix.DB())

	// getCsEnv() is a plain function call — should always work.
	t.Run("direct_function_call", func(t *testing.T) {
		res, err := reader.GetReferences(ctx, "getCsEnv", "", 20, nil)
		if err != nil {
			t.Fatalf("GetReferences: %v", err)
		}
		hits := res.Matches
		if len(hits) == 0 {
			t.Fatal("expected at least one reference to getCsEnv; got 0")
		}
		var found bool
		for _, h := range hits {
			if strings.Contains(h.SrcPath, "spec.ts") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected hit in spec.ts; got %+v", hits)
		}
	})

	// setCsEnv() is a plain function call — should always work.
	t.Run("direct_function_call_setCsEnv", func(t *testing.T) {
		res, err := reader.GetReferences(ctx, "setCsEnv", "", 20, nil)
		if err != nil {
			t.Fatalf("GetReferences: %v", err)
		}
		hits := res.Matches
		if len(hits) == 0 {
			t.Fatal("expected at least one reference to setCsEnv; got 0")
		}
	})

	// CsEnv.from() and CsEnv.empty() are static method calls. The TS resolver
	// resolves them to the *method* symbols ("env.CsEnv.from", "env.CsEnv.empty"),
	// not to the class symbol ("env.CsEnv"). get_references("CsEnv") must
	// therefore also include child-symbol IDs so it finds these calls.
	t.Run("class_static_method_calls_visible_via_class_name", func(t *testing.T) {
		res, err := reader.GetReferences(ctx, "CsEnv", "", 20, nil)
		if err != nil {
			t.Fatalf("GetReferences: %v", err)
		}
		hits := res.Matches
		if len(hits) == 0 {
			t.Fatal("expected references to CsEnv (via CsEnv.from/CsEnv.empty calls); got 0 — " +
				"symbolsByTarget must include child method symbols when parent class is the target")
		}
		var foundFrom, foundEmpty bool
		for _, h := range hits {
			if strings.Contains(h.DstName, "CsEnv.from") || strings.Contains(h.DstName, "env.CsEnv.from") {
				foundFrom = true
			}
			if strings.Contains(h.DstName, "CsEnv.empty") || strings.Contains(h.DstName, "env.CsEnv.empty") {
				foundEmpty = true
			}
		}
		if !foundFrom {
			t.Errorf("expected a hit for CsEnv.from() call; hits: %+v", hits)
		}
		if !foundEmpty {
			t.Errorf("expected a hit for CsEnv.empty() call; hits: %+v", hits)
		}
	})
}
