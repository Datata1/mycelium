package query

import "testing"

func TestCutPreview(t *testing.T) {
	const file = "l1\nl2\nl3\nl4\nl5\n"
	cases := []struct {
		name          string
		raw           string
		cut           int
		wantContent   string
		wantTotal     int
		wantTruncated bool
	}{
		{"under cap returns whole file", file, 10, file, 5, false},
		{"at cap returns whole file", file, 5, file, 5, false},
		{"over cap truncates", file, 2, "l1\nl2\n", 5, true},
		{"zero cap returns whole file", file, 0, file, 5, false},
		{"negative cap returns whole file", file, -1, file, 5, false},
		{"empty file", "", 3, "", 0, false},
		{"no trailing newline counted", "a\nb", 1, "a\n", 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content, total, truncated := cutPreview(c.raw, c.cut)
			if content != c.wantContent {
				t.Errorf("content = %q, want %q", content, c.wantContent)
			}
			if total != c.wantTotal {
				t.Errorf("total = %d, want %d", total, c.wantTotal)
			}
			if truncated != c.wantTruncated {
				t.Errorf("truncated = %v, want %v", truncated, c.wantTruncated)
			}
		})
	}
}

func TestApplyFocusToHits(t *testing.T) {
	hits := []SymbolHit{
		{Name: "ParseConfig", Qualified: "config.ParseConfig"},
		{Name: "Login", Qualified: "auth.Login"},
		{Name: "parseHeader", Qualified: "http.parseHeader"},
	}
	got := applyFocusToHits([]string{"parse"}, hits)
	if len(got) == 0 {
		t.Fatal("focus 'parse' dropped everything")
	}
	for _, h := range got {
		if h.Name == "Login" {
			t.Errorf("non-matching hit %q survived the focus filter", h.Name)
		}
	}

	// A focus token matching nothing yields an empty (non-nil) result.
	none := applyFocusToHits([]string{"zzzznomatch"}, hits)
	if len(none) != 0 {
		t.Errorf("expected no survivors, got %d", len(none))
	}
}
