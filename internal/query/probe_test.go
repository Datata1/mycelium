package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func probeFixture(t *testing.T) (*FSProbe, string) {
	t.Helper()
	root := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("pkg/a.go", "package pkg\n")
	mustWrite("notes.md", "# notes\n")
	mustWrite("testdata/fixture.go", "package fixture\n")
	mustWrite("pkg/huge.go", strings.Repeat("x", 2048))
	return &FSProbe{
		Root:          root,
		Include:       []string{"**/*.go"},
		Exclude:       []string{"**/testdata/**"},
		MaxFileSizeKB: 1,
	}, root
}

func TestFSProbe_DiagnosePath(t *testing.T) {
	t.Parallel()
	probe, _ := probeFixture(t)
	scan := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		rel  string
		want string // substring the (single) hint must carry
	}{
		{"pkg/missing.go", "not on disk either"},
		{"testdata/fixture.go", `exclude pattern "**/testdata/**"`},
		{"notes.md", "does not match any include glob"},
		{"pkg/huge.go", "over the max_file_size_kb=1 cap"},
		{"pkg/a.go", "the index is stale"},
	}
	for _, tc := range cases {
		hints := probe.DiagnosePath(tc.rel, scan)
		if len(hints) != 1 {
			t.Errorf("%s: got %d hints (%v), want 1", tc.rel, len(hints), hints)
			continue
		}
		if !strings.Contains(hints[0], tc.want) {
			t.Errorf("%s: hint %q missing %q", tc.rel, hints[0], tc.want)
		}
	}

	// Stale hint on an index that never reconciled names that fact.
	hints := probe.DiagnosePath("pkg/a.go", time.Time{})
	if len(hints) != 1 || !strings.Contains(hints[0], "no full reconcile recorded") {
		t.Errorf("zero scan time: got %v", hints)
	}

	// Nil probe is a silent no-op everywhere.
	var nilProbe *FSProbe
	if got := nilProbe.DiagnosePath("pkg/a.go", scan); got != nil {
		t.Errorf("nil probe: got %v", got)
	}
}

func TestBuildRefsHints(t *testing.T) {
	t.Parallel()
	if h := buildRefsHints("Logn", 0, false); len(h) != 1 ||
		!strings.Contains(h[0], `no symbol or reference named "Logn"`) {
		t.Errorf("unknown symbol: %v", h)
	}
	if h := buildRefsHints("Login", 2, false); len(h) != 1 ||
		!strings.Contains(h[0], "symbol exists (2 definition(s)) but has no indexed references") {
		t.Errorf("dead code: %v", h)
	}
	if h := buildRefsHints("Login", 0, true); len(h) != 2 ||
		!strings.Contains(h[1], "never completed a full reconcile") {
		t.Errorf("never reconciled: %v", h)
	}
}

func TestBuildLexicalHints(t *testing.T) {
	t.Parallel()
	if h := buildLexicalHints("AuthService", true, 0); len(h) != 1 ||
		!strings.Contains(h[0], `find_symbol("AuthService")`) {
		t.Errorf("identifier: %v", h)
	}
	if h := buildLexicalHints(`login failed: \d+`, false, 3); len(h) != 1 ||
		!strings.Contains(h[0], "3 indexed file(s) missing on disk") {
		t.Errorf("missing files: %v", h)
	}
	if h := buildLexicalHints("some phrase", false, 0); len(h) != 0 {
		t.Errorf("nothing to say: %v", h)
	}
}

func TestIdentifierShaped(t *testing.T) {
	t.Parallel()
	for _, yes := range []string{"AuthService", "auth.AuthService.Login", "_private", "x2"} {
		if !identifierShaped(yes) {
			t.Errorf("%q should be identifier-shaped", yes)
		}
	}
	for _, no := range []string{`login failed: \d+`, "a b", "foo|bar", "(?i)x", "1abc", ""} {
		if identifierShaped(no) {
			t.Errorf("%q should NOT be identifier-shaped", no)
		}
	}
}
