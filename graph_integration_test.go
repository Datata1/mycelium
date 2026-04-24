// Integration tests for v1.6 graph-native tools and the --since filter
// (reader-level). Runs against the committed sample fixture so we have
// a known ref graph to assert on.
package mycelium_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/parser/python"
	"github.com/jdwiederstein/mycelium/internal/parser/typescript"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
	pyresolver "github.com/jdwiederstein/mycelium/internal/resolver/python"
	tsresolver "github.com/jdwiederstein/mycelium/internal/resolver/typescript"
)

func setupGraphFixture(t *testing.T) (string, *query.Reader) {
	t.Helper()
	dst := copyFixture(t, "testdata/fixtures/sample")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	t.Cleanup(func() { ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(golang.New())
	reg.Register(typescript.New())
	reg.Register(python.New())

	walker := repo.NewWalker(
		dst,
		[]string{"**/*.go", "src/**/*.ts", "py/**/*.py"},
		nil,
		0,
	)
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   walker,
		Embedder: embed.Noop{},
		Resolvers: map[string]pipeline.Resolver{
			"typescript": tsresolver.New(),
			"python":     pyresolver.New(),
		},
	}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}
	return dst, query.NewReader(ix.DB())
}

// TestIntegration_ImpactAnalysis verifies the transitive inbound
// closure on a known TS chain:
//   issueToken -> fingerprint -> normalizeEmail
// Seeding on normalizeEmail, distance 1 must include fingerprint and
// distance 2 must include issueToken.
func TestIntegration_ImpactAnalysis(t *testing.T) {
	t.Parallel()
	_, reader := setupGraphFixture(t)
	ctx := context.Background()

	imp, err := reader.ImpactAnalysis(ctx, "auth.normalizeEmail", "", "", 5, nil)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	if imp.Seed.Qualified != "auth.normalizeEmail" {
		t.Fatalf("seed = %q, want auth.normalizeEmail", imp.Seed.Qualified)
	}

	byName := map[string]int{}
	for _, h := range imp.Hits {
		byName[h.Qualified] = h.Distance
	}
	if d := byName["auth.AuthService.fingerprint"]; d != 1 {
		t.Errorf("fingerprint distance = %d, want 1 (direct caller)", d)
	}
	if d := byName["auth.AuthService.issueToken"]; d != 2 {
		t.Errorf("issueToken distance = %d, want 2 (through fingerprint)", d)
	}

	t.Run("kind_filter_narrows", func(t *testing.T) {
		// With kind=method both TS symbols should still surface (they're
		// both methods); with an impossible kind the result is empty.
		imp2, err := reader.ImpactAnalysis(ctx, "auth.normalizeEmail", "nonsense-kind", "", 5, nil)
		if err != nil {
			t.Fatalf("impact kind: %v", err)
		}
		if len(imp2.Hits) != 0 {
			t.Errorf("kind filter should have zeroed out hits; got %d", len(imp2.Hits))
		}
	})

	t.Run("depth_clamp_note", func(t *testing.T) {
		imp3, err := reader.ImpactAnalysis(ctx, "auth.normalizeEmail", "", "", 999, nil)
		if err != nil {
			t.Fatalf("impact overdepth: %v", err)
		}
		if len(imp3.Notes) == 0 {
			t.Errorf("expected a depth-clamp note when requesting depth 999")
		}
	})
}

// TestIntegration_CriticalPath asserts that critical_path finds the
// known path issueToken -> fingerprint -> normalizeEmail. Exercises
// the SQL CTE + hydrate stitching.
func TestIntegration_CriticalPath(t *testing.T) {
	t.Parallel()
	_, reader := setupGraphFixture(t)
	ctx := context.Background()

	cp, err := reader.CriticalPath(ctx, "auth.AuthService.issueToken", "auth.normalizeEmail", "", 0, 0)
	if err != nil {
		t.Fatalf("critical path: %v", err)
	}
	if len(cp.Paths) == 0 {
		t.Fatalf("expected at least one path from issueToken to normalizeEmail; got none")
	}

	// One path should be issueToken -> fingerprint -> normalizeEmail
	// (3 vertices, 2 hops). Ordering is by length so that path is
	// either the only one or the shortest one.
	shortest := cp.Paths[0]
	if len(shortest) < 3 {
		t.Fatalf("shortest path has %d vertices, want >=3", len(shortest))
	}
	want := []string{"auth.AuthService.issueToken", "auth.AuthService.fingerprint", "auth.normalizeEmail"}
	got := make([]string, 0, len(shortest))
	for _, v := range shortest {
		got = append(got, v.Qualified)
	}
	found := false
	for _, p := range cp.Paths {
		if len(p) != len(want) {
			continue
		}
		match := true
		for i := range want {
			if p[i].Qualified != want[i] {
				match = false
				break
			}
		}
		if match {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected path %v in returned paths; got shortest=%v", want, got)
	}
}

// TestIntegration_PathsInFilter verifies that the reader-level pathsIn
// filter scopes FindSymbol to a single file. No git process required.
func TestIntegration_PathsInFilter(t *testing.T) {
	t.Parallel()
	_, reader := setupGraphFixture(t)
	ctx := context.Background()

	// Unscoped: Greeter lives in main.go.
	all, err := reader.FindSymbol(ctx, "Greeter", "", "", 10, nil)
	if err != nil {
		t.Fatalf("find unscoped: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("expected Greeter in unscoped results")
	}

	t.Run("match_file", func(t *testing.T) {
		hits, err := reader.FindSymbol(ctx, "Greeter", "", "", 10, []string{"main.go"})
		if err != nil {
			t.Fatalf("find scoped: %v", err)
		}
		if len(hits) == 0 {
			t.Errorf("expected Greeter hit when pathsIn contains main.go")
		}
		for _, h := range hits {
			if !strings.HasSuffix(h.Path, "main.go") {
				t.Errorf("unexpected path in scoped result: %s", h.Path)
			}
		}
	})

	t.Run("nonmatching_file", func(t *testing.T) {
		hits, err := reader.FindSymbol(ctx, "Greeter", "", "", 10, []string{"src/auth.ts"})
		if err != nil {
			t.Fatalf("find scoped: %v", err)
		}
		if len(hits) != 0 {
			t.Errorf("expected 0 hits when pathsIn excludes main.go; got %d", len(hits))
		}
	})

	t.Run("empty_slice_is_zero_rows", func(t *testing.T) {
		// Empty (non-nil) slice = "--since matched nothing" — must return 0.
		hits, err := reader.FindSymbol(ctx, "Greeter", "", "", 10, []string{})
		if err != nil {
			t.Fatalf("find empty: %v", err)
		}
		if len(hits) != 0 {
			t.Errorf("empty pathsIn should yield 0 hits; got %d", len(hits))
		}
	})
}
