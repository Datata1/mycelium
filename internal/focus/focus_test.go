package focus

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"the of and", nil},
		{"Auth", []string{"auth"}},
		{"login auth", []string{"login", "auth"}},
		{"auth login auth", []string{"auth", "login"}},
		{"compile-skills tree", []string{"compile", "skills", "tree"}},
		{"a function for the auth flow", []string{"function", "auth", "flow"}},
		{"FindSymbol", []string{"findsymbol"}},
		{"emit_skills", []string{"emit", "skills"}},
	}
	for _, c := range cases {
		got := Tokenize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMatch_EmptyFocusKeepsAll(t *testing.T) {
	score, ok := Match("", Candidate{Name: "anything"})
	if !ok || score != 0 {
		t.Fatalf("empty focus: got (%v, %v), want (0, true)", score, ok)
	}
	score, ok = Match("the of", Candidate{Name: "anything"})
	if !ok || score != 0 {
		t.Fatalf("only-stopword focus: got (%v, %v), want (0, true)", score, ok)
	}
}

func TestMatch_ScoreOrder(t *testing.T) {
	exact, _ := Match("auth", Candidate{Name: "auth"})
	substr, _ := Match("auth", Candidate{Name: "authservice"})
	doc, _ := Match("auth", Candidate{Docstring: "handles auth flow"})
	ref, _ := Match("auth", Candidate{RefTargets: []string{"auth.NormalizeEmail"}})
	none, ok := Match("auth", Candidate{Name: "unrelated"})
	if ok {
		t.Fatalf("non-match should be ok=false, got score %v", none)
	}
	if !(exact > substr && substr > doc && doc > ref) {
		t.Fatalf("score order broken: exact=%v substr=%v doc=%v ref=%v", exact, substr, doc, ref)
	}
}

func TestMatch_MultiToken(t *testing.T) {
	// Both tokens hit the name → 2 * substring score.
	score, ok := Match("auth login", Candidate{Name: "authLogin"})
	if !ok || score != 2*ScoreNameSubstring {
		t.Fatalf("auth login on authLogin: got %v, want %v", score, 2*ScoreNameSubstring)
	}
	// One token name-matches, other doc-matches.
	score, ok = Match("auth flow", Candidate{Name: "auth", Docstring: "main flow entry"})
	if !ok || score != ScoreNameExact+ScoreDocSubstring {
		t.Fatalf("multi-source: got %v, want %v", score, ScoreNameExact+ScoreDocSubstring)
	}
}

func TestMatch_QualifiedSubstring(t *testing.T) {
	score, ok := Match("query", Candidate{Name: "Reader", Qualified: "internal/query.Reader"})
	if !ok || score != ScoreNameSubstring {
		t.Fatalf("qualified-only match: got %v, want %v", score, ScoreNameSubstring)
	}
}

func TestMatch_RefTargetsCaseInsensitive(t *testing.T) {
	score, ok := Match("normalize", Candidate{
		Name:       "issueToken",
		RefTargets: []string{"Auth.NormalizeEmail"},
	})
	if !ok || score != ScoreRefSubstring {
		t.Fatalf("ref-target match: got %v, want %v", score, ScoreRefSubstring)
	}
}

func TestMatch_PriorityFirstHitWins(t *testing.T) {
	// A token that could hit both name and ref should only score once,
	// at the highest tier (name > doc > ref). This keeps a candidate
	// with a strong name match from being inflated by incidental refs.
	score, ok := Match("auth", Candidate{
		Name:       "auth",
		Docstring:  "auth helper",
		RefTargets: []string{"auth.X"},
	})
	if !ok || score != ScoreNameExact {
		t.Fatalf("priority first-hit: got %v, want %v", score, ScoreNameExact)
	}
}

func TestMatchTokens_SkipsRetokenization(t *testing.T) {
	tokens := Tokenize("auth login")
	c := Candidate{Name: "authservice", Docstring: "handles login"}
	score, ok := MatchTokens(tokens, c)
	if !ok || score != ScoreNameSubstring+ScoreDocSubstring {
		t.Fatalf("got %v, want %v", score, ScoreNameSubstring+ScoreDocSubstring)
	}
}

func TestMatch_UnicodeIdentifiers(t *testing.T) {
	// Tokenizer keeps unicode letters, so non-ASCII identifiers still match.
	score, ok := Match("café", Candidate{Name: "Café"})
	if !ok || score != ScoreNameExact {
		t.Fatalf("unicode exact: got %v, want %v", score, ScoreNameExact)
	}
}
