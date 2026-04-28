// Integration tests for v2.4 Pillar I (focused reads). Exercises the
// `focus` parameter on FindSymbol / GetFileOutline / GetNeighborhood
// and the new ReadFocused method against the existing sample fixture.
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

// setupFocusFixture mirrors TestIntegration_IndexAndQuery's setup
// without coupling to its assertions. Returns the repo root + a Reader.
func setupFocusFixture(t *testing.T) (string, *query.Reader) {
	t.Helper()
	dst := copyFixture(t, "testdata/fixtures/sample")
	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	t.Cleanup(func() { ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(golang.New())
	reg.Register(typescript.New())
	reg.Register(python.New())

	walker := repo.NewWalker(dst, []string{"**/*.go", "src/**/*.ts", "py/**/*.py"}, nil, 0)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}
	return dst, query.NewReader(ix.DB())
}

func TestIntegration_FindSymbol_Focus(t *testing.T) {
	t.Parallel()
	_, reader := setupFocusFixture(t)
	ctx := context.Background()

	// Empty focus = baseline behaviour.
	baselineRes, err := reader.FindSymbol(ctx, "", "", "", 50, nil, "")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	baseline := baselineRes.Matches
	if len(baseline) == 0 {
		t.Fatalf("expected baseline hits")
	}

	// Focus="auth" should drop unrelated symbols and rank auth-named ones first.
	focusedRes, err := reader.FindSymbol(ctx, "", "", "", 50, nil, "auth")
	if err != nil {
		t.Fatalf("focused: %v", err)
	}
	focused := focusedRes.Matches
	if len(focused) == 0 {
		t.Fatalf("expected at least one focused hit")
	}
	if len(focused) >= len(baseline) {
		t.Errorf("focused result should be a strict subset; got focused=%d baseline=%d",
			len(focused), len(baseline))
	}
	// First hit must contain the focus token.
	first := strings.ToLower(focused[0].Qualified)
	if !strings.Contains(first, "auth") {
		t.Errorf("first focused hit %q does not mention 'auth'", focused[0].Qualified)
	}
}

func TestIntegration_GetFileOutline_Focus(t *testing.T) {
	t.Parallel()
	_, reader := setupFocusFixture(t)
	ctx := context.Background()

	// Baseline: main.go has Greeter, NewGreeter, main, etc.
	baseline, err := reader.GetFileOutline(ctx, "main.go", "")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if len(baseline) < 2 {
		t.Fatalf("expected at least 2 top-level items; got %d", len(baseline))
	}

	// Focus on "greeter" should drop the bare main() entry.
	focused, err := reader.GetFileOutline(ctx, "main.go", "greeter")
	if err != nil {
		t.Fatalf("focused: %v", err)
	}
	for _, it := range focused {
		if !strings.Contains(strings.ToLower(it.Qualified), "greeter") {
			// Method/child match is OK — it'd surface via a Greeter-named
			// parent. Top-level non-greeter items should not appear.
			t.Errorf("unexpected non-greeter top-level item: %s", it.Qualified)
		}
	}
	if len(focused) == 0 {
		t.Errorf("expected greeter-related items; got 0")
	}

	// Focus that matches nothing in main.go should drop everything.
	empty, err := reader.GetFileOutline(ctx, "main.go", "thisDoesNotExist")
	if err != nil {
		t.Fatalf("no-match focus: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 items for non-matching focus; got %d", len(empty))
	}
}

func TestIntegration_GetNeighborhood_Focus(t *testing.T) {
	t.Parallel()
	_, reader := setupFocusFixture(t)
	ctx := context.Background()

	// Baseline: NewGreeter inbound.
	baseline, err := reader.GetNeighborhood(ctx, "NewGreeter", "", 2, query.DirIn, "")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}

	// Focus on something the seed name itself contains keeps the seed.
	focused, err := reader.GetNeighborhood(ctx, "NewGreeter", "", 2, query.DirIn, "greeter")
	if err != nil {
		t.Fatalf("focused: %v", err)
	}
	if focused.Seed.Qualified != baseline.Seed.Qualified {
		t.Errorf("seed changed: got %q want %q", focused.Seed.Qualified, baseline.Seed.Qualified)
	}

	// Focus that matches nothing reachable should still keep the seed
	// itself and surface a prune note.
	pruned, err := reader.GetNeighborhood(ctx, "NewGreeter", "", 2, query.DirIn, "noSuchToken")
	if err != nil {
		t.Fatalf("pruned: %v", err)
	}
	if len(pruned.Nodes) != 1 || pruned.Nodes[0].ID != pruned.Seed.ID {
		t.Errorf("expected only seed to remain; got nodes=%v", qualifiedNames(pruned.Nodes))
	}
	if !hasPruneNote(pruned.Notes) {
		t.Errorf("expected a prune note; got %v", pruned.Notes)
	}
}

func TestIntegration_ReadFocused(t *testing.T) {
	t.Parallel()
	dst, reader := setupFocusFixture(t)
	ctx := context.Background()

	// Empty focus = full content (collapse no-op).
	full, err := reader.ReadFocused(ctx, dst, "main.go", "")
	if err != nil {
		t.Fatalf("full read: %v", err)
	}
	if full.Stats.OriginalBytes == 0 {
		t.Fatalf("expected non-zero original bytes")
	}
	if full.Stats.ExpandedSymbols != full.Stats.TotalSymbols {
		t.Errorf("empty focus must expand all: got %d/%d",
			full.Stats.ExpandedSymbols, full.Stats.TotalSymbols)
	}

	// Focus on "greeter": Greet/NewGreeter/Greeter survive; main() collapses.
	focused, err := reader.ReadFocused(ctx, dst, "main.go", "greeter")
	if err != nil {
		t.Fatalf("focused read: %v", err)
	}
	if focused.Stats.ExpandedSymbols == 0 {
		t.Fatalf("expected expanded symbols for 'greeter'")
	}
	if focused.Stats.ExpandedSymbols >= focused.Stats.TotalSymbols {
		t.Errorf("expected fewer expansions than total; got %d/%d",
			focused.Stats.ExpandedSymbols, focused.Stats.TotalSymbols)
	}
	if focused.Stats.ReturnedBytes >= focused.Stats.OriginalBytes {
		t.Errorf("expected bytes saved; got returned=%d original=%d",
			focused.Stats.ReturnedBytes, focused.Stats.OriginalBytes)
	}
	if !strings.Contains(focused.Content, "// collapsed (lines") {
		t.Errorf("expected a collapse marker in output; got:\n%s", focused.Content)
	}
	// Expanded list should map back to source line ranges.
	for _, e := range focused.Expanded {
		if e.StartLine == 0 || e.EndLine < e.StartLine {
			t.Errorf("expanded entry has bad line range: %+v", e)
		}
	}

	// Unknown file is a clean error, not a panic.
	if _, err := reader.ReadFocused(ctx, dst, "no-such.go", "auth"); err == nil {
		t.Errorf("expected error for unknown file")
	}
}

func qualifiedNames(nodes []query.NeighborNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Qualified
	}
	return out
}

func hasPruneNote(notes []string) bool {
	for _, n := range notes {
		if strings.Contains(n, "focus filter pruned") {
			return true
		}
	}
	return false
}
