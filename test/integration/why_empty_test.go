// Integration tests for the WS03 why-empty surface: misses explain
// themselves (unknown symbol, dead code, stale index, excluded path)
// instead of returning bare empties that push agents back to grep.
package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
)

// whyEmptyFixture indexes a small Go-only tree and attaches the probe,
// mirroring how cmd/myco wires it from config.
func whyEmptyFixture(t *testing.T) (*query.Reader, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"pkg/greet.go": "package pkg\n\nfunc Greet() string { return Farewell() }\n\nfunc Farewell() string { return \"bye\" }\n\nfunc Unused() string { return \"dead\" }\n",
		"notes.md":     "# not indexed\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ix := openIndex(t, filepath.Join(root, ".mycelium", "index.db"))
	t.Cleanup(func() { _ = ix.Close() })
	reg := parser.NewRegistry()
	reg.Register(golang.New())
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   repo.NewWalker(root, []string{"**/*.go"}, []string{"**/testdata/**"}, 0),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}

	reader := query.NewReader(ix.DB())
	reader.SetProbe(&query.FSProbe{
		Root:    root,
		Include: []string{"**/*.go"},
		Exclude: []string{"**/testdata/**"},
	})
	return reader, root
}

func wantSubstring(t *testing.T, got []string, sub string) {
	t.Helper()
	if !strings.Contains(strings.Join(got, "\n"), sub) {
		t.Errorf("hints %v missing %q", got, sub)
	}
}

func TestIntegration_WhyEmpty(t *testing.T) {
	t.Parallel()
	reader, root := whyEmptyFixture(t)
	ctx := context.Background()

	t.Run("refs_unknown_symbol", func(t *testing.T) {
		res, err := reader.GetReferences(ctx, "Greeet", "", 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Matches) != 0 {
			t.Fatalf("unexpected matches: %+v", res.Matches)
		}
		wantSubstring(t, res.Hints, `no symbol or reference named "Greeet"`)
	})

	t.Run("refs_symbol_without_references", func(t *testing.T) {
		res, err := reader.GetReferences(ctx, "Unused", "", 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Matches) != 0 {
			t.Fatalf("unexpected matches: %+v", res.Matches)
		}
		wantSubstring(t, res.Hints, "symbol exists (1 definition(s)) but has no indexed references")
	})

	t.Run("refs_hit_has_no_hints", func(t *testing.T) {
		res, err := reader.GetReferences(ctx, "Farewell", "", 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Matches) == 0 {
			t.Fatal("expected the Greet→Farewell reference")
		}
		if len(res.Hints) != 0 {
			t.Errorf("hits must not carry hints: %v", res.Hints)
		}
	})

	t.Run("lexical_identifier_pattern_redirects", func(t *testing.T) {
		res, err := reader.SearchLexical(ctx, "NoSuchIdentifier", "", "", 10, root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Matches) != 0 {
			t.Fatalf("unexpected matches: %+v", res.Matches)
		}
		wantSubstring(t, res.Hints, `find_symbol("NoSuchIdentifier")`)
	})

	t.Run("lexical_missing_on_disk_is_reported", func(t *testing.T) {
		// Delete an indexed file without reconciling — the watcher-lost
		// -event scenario. The miss must name the staleness.
		if err := os.Remove(filepath.Join(root, "pkg/greet.go")); err != nil {
			t.Fatal(err)
		}
		res, err := reader.SearchLexical(ctx, "zzz_not_in_any_file", "", "", 10, root, nil)
		if err != nil {
			t.Fatal(err)
		}
		wantSubstring(t, res.Hints, "1 indexed file(s) missing on disk")
		// Restore for the remaining subtests.
		if err := os.WriteFile(filepath.Join(root, "pkg/greet.go"),
			[]byte("package pkg\n\nfunc Greet() string { return Farewell() }\n\nfunc Farewell() string { return \"bye\" }\n\nfunc Unused() string { return \"dead\" }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("read_focused_uninindexed_extension_explains", func(t *testing.T) {
		_, err := reader.ReadFocused(ctx, root, "notes.md", "")
		if err == nil {
			t.Fatal("expected not-found error")
		}
		if !strings.Contains(err.Error(), "does not match any include glob") {
			t.Errorf("error %q missing include-glob diagnosis", err)
		}
	})

	t.Run("outline_unknown_path_is_error_with_diagnosis", func(t *testing.T) {
		_, err := reader.GetFileOutline(ctx, "pkg/nope.go", "")
		if err == nil {
			t.Fatal("expected not-found error")
		}
		if !strings.Contains(err.Error(), "not on disk either") {
			t.Errorf("error %q missing on-disk diagnosis", err)
		}
	})

	t.Run("summary_unknown_path_is_error", func(t *testing.T) {
		_, err := reader.GetFileSummary(ctx, "pkg/nope.go")
		if err == nil {
			t.Fatal("expected not-found error (was a silent zero-value summary)")
		}
	})
}
