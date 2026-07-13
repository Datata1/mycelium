package main

import (
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/ipc"
)

func primeStats() ipc.Stats {
	return ipc.Stats{
		Files:               156,
		Symbols:             1057,
		Refs:                7343,
		NonImportRefs:       1700,
		RefsTrulyUnresolved: 64,
		ByLang:              map[string]int{"go": 155, "typescript": 36, "": 1},
		LastScan:            time.Date(2026, 7, 4, 11, 0, 0, 0, time.UTC),
		// LastFullScan must win over the older LastScan below.
		LastFullScan: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
	}
}

func TestPrimeContext_Content(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 4, 12, 2, 0, 0, time.UTC)
	text, ok := primeContext(primeStats(), now)
	if !ok {
		t.Fatal("expected ok for a populated index")
	}
	want := "myco (MCP) is indexing this repo: 156 files (go 155, typescript 36), " +
		"1057 symbols, refs 96% resolved, last scan 2m ago. " +
		"Rules: identifier → find_symbol (never search_lexical); " +
		"callers → get_references; read a file → read_focused(path, focus=...); " +
		"orientation → get_file_outline / get_file_summary; " +
		"blast radius → impact_analysis; document keys (i18n, deps) → find_document_key; " +
		"after edits & before declaring done → verify_changes; " +
		"which tests to run → select_tests. " +
		"search_lexical is ONLY for literal strings/regex. " +
		"Pass returned path+project values verbatim — never prepend the repo root."
	if text != want {
		t.Errorf("prime text drifted:\ngot:  %s\nwant: %s", text, want)
	}
}

// The block is injected into every session: hold it to a hard character
// budget (~250 tokens ≈ 1000 chars) so it never becomes a context tax.
func TestPrimeContext_Budget(t *testing.T) {
	t.Parallel()
	s := primeStats()
	s.ByLang = map[string]int{"go": 1, "typescript": 2, "python": 3}
	text, _ := primeContext(s, time.Now())
	if len(text) > 1000 {
		t.Errorf("prime text is %d chars — budget is 1000 (~250 tokens)", len(text))
	}
}

func TestPrimeContext_EmptyIndexIsSilent(t *testing.T) {
	t.Parallel()
	if _, ok := primeContext(ipc.Stats{}, time.Now()); ok {
		t.Error("empty index must not prime")
	}
}

func TestPrimeAge_Buckets(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, ", last scan just now"},
		{5 * time.Minute, ", last scan 5m ago"},
		{3 * time.Hour, ", last scan 3h ago"},
		{72 * time.Hour, ", last scan 3d ago"},
	}
	for _, tc := range cases {
		if got := primeAge(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("age %v: got %q, want %q", tc.ago, got, tc.want)
		}
	}
	if got := primeAge(time.Time{}, now); got != "" {
		t.Errorf("zero time: got %q, want empty", got)
	}
}

func TestPrimeLangs_SortedAndFiltered(t *testing.T) {
	t.Parallel()
	got := primeLangs(map[string]int{"python": 3, "go": 10, "": 4, "typescript": 3})
	if got != "go 10, python 3, typescript 3" {
		t.Errorf("langs = %q", got)
	}
	if got := primeLangs(nil); got != "no languages" {
		t.Errorf("empty langs = %q", got)
	}
}
