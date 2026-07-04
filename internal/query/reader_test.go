package query

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/languages"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/repo"
)

// newFixtureReader indexes files (relPath -> content) into a fresh
// temp-dir SQLite index and returns a Reader over it. No resolver is
// wired, so refs stay textual — enough for the read-side assertions here.
func newFixtureReader(t *testing.T, files map[string]string) (*Reader, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	ix, err := index.Open(filepath.Join(root, ".mycelium", "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })

	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: languages.Registry([]string{"go"}),
		Walker:   repo.NewWalker(root, []string{"**/*.go"}, nil, 0),
	}
	if _, err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("index: %v", err)
	}
	return NewReader(ix.DB()), root
}

const fxService = `package auth

// Login authenticates a user.
func Login(user string) error { return nil }

type Session struct{ ID string }

func (s *Session) Close() error { return nil }
`

const fxCaller = `package app

import "example/auth"

func Run() error { return auth.Login("bob") }
`

func TestReaderFindSymbol(t *testing.T) {
	r, _ := newFixtureReader(t, map[string]string{
		"auth/service.go": fxService,
		"app/run.go":      fxCaller,
	})
	ctx := context.Background()

	t.Run("exact match ranks first", func(t *testing.T) {
		res, err := r.FindSymbol(ctx, "Login", "", "", 10, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Matches) == 0 {
			t.Fatal("no matches for Login")
		}
		if res.Matches[0].Name != "Login" {
			t.Errorf("first hit = %q, want Login", res.Matches[0].Name)
		}
	})

	t.Run("kind filter", func(t *testing.T) {
		res, err := r.FindSymbol(ctx, "Session", "type", "", 10, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Matches) != 1 || res.Matches[0].Kind != "type" {
			t.Fatalf("want one type match, got %+v", res.Matches)
		}
	})

	t.Run("miss returns non-nil empty matches and a kind hint", func(t *testing.T) {
		res, err := r.FindSymbol(ctx, "Login", "interface", "", 10, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Matches == nil {
			t.Error("Matches must be non-nil (serialises as [] not null)")
		}
		if len(res.Matches) != 0 {
			t.Fatalf("want zero matches, got %+v", res.Matches)
		}
		if len(res.Hints) == 0 {
			t.Error("expected a hint explaining the kind filter eliminated matches")
		}
	})
}

func TestReaderGetReferences(t *testing.T) {
	r, _ := newFixtureReader(t, map[string]string{
		"auth/service.go": fxService,
		"app/run.go":      fxCaller,
	})
	res, err := r.GetReferences(context.Background(), "Login", "", 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	hits := res.Matches
	if len(hits) == 0 {
		t.Fatal("expected at least one reference to Login")
	}
	found := false
	for _, h := range hits {
		if h.SrcPath == "app/run.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a reference from app/run.go, got %+v", hits)
	}
}

func TestReaderStats(t *testing.T) {
	r, _ := newFixtureReader(t, map[string]string{"auth/service.go": fxService})
	s, err := r.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Files != 1 {
		t.Errorf("files = %d, want 1", s.Files)
	}
	if s.Symbols == 0 {
		t.Error("expected symbols > 0")
	}
	if s.ByLang["go"] == 0 {
		t.Errorf("expected go symbols, got ByLang=%v", s.ByLang)
	}
}

func TestReaderReadFocusedPreviewCap(t *testing.T) {
	// A file longer than the cap so the no-focus preview truncates.
	body := "package big\n"
	for i := 0; i < 40; i++ {
		body += "// filler line to exceed the preview cap\n"
	}
	body += "func Big() {}\n"

	r, root := newFixtureReader(t, map[string]string{"big/big.go": body})
	r.SetReadPreviewLines(10)

	fr, err := r.ReadFocused(context.Background(), root, "big/big.go", "")
	if err != nil {
		t.Fatal(err)
	}
	if fr.Hint == "" {
		t.Error("expected a preview-truncation hint")
	}
	if fr.Stats.ReturnedBytes >= fr.Stats.OriginalBytes {
		t.Errorf("preview should return fewer bytes: returned=%d original=%d",
			fr.Stats.ReturnedBytes, fr.Stats.OriginalBytes)
	}
}
