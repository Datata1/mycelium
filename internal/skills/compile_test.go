package skills

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jdwiederstein/mycelium/internal/query"
)

// updateGolden lets a developer regenerate the expected output:
//
//	go test -tags sqlite_fts5 ./internal/skills/ -update
//
// The flag is package-local; goldens live under
// testdata/golden/ next to this file.
var updateGolden = flag.Bool("update", false, "regenerate testdata golden files")

// fakeReader implements the skills.Reader interface against fixtures
// declared inline. Avoids spinning up a SQLite index for the golden
// test — the unit under test is the deterministic markdown emitter,
// not the query layer.
type fakeReader struct {
	files     []query.FileHit
	summaries map[string]query.FileSummary
	outlines  map[string][]query.FileOutlineItem
	inbound   map[string][]query.PackageRefAgg
	outbound  map[string][]query.PackageRefAgg
	aspectFn  fakeAspectFn
}

func (f *fakeReader) ListFiles(ctx context.Context, language, nameContains, project string, limit int, pathsIn []string) ([]query.FileHit, error) {
	if language == "" {
		return f.files, nil
	}
	out := f.files[:0:0]
	for _, fh := range f.files {
		if fh.Language == language {
			out = append(out, fh)
		}
	}
	return out, nil
}

func (f *fakeReader) GetFileSummary(ctx context.Context, p string) (query.FileSummary, error) {
	return f.summaries[p], nil
}

func (f *fakeReader) GetFileOutline(ctx context.Context, p string) ([]query.FileOutlineItem, error) {
	return f.outlines[p], nil
}

func (f *fakeReader) PackageRefAggregates(ctx context.Context, pkgDir string, limit int) ([]query.PackageRefAgg, []query.PackageRefAgg, error) {
	return f.inbound[pkgDir], f.outbound[pkgDir], nil
}

// aspectMatches lets a fixture pin the AspectMatch slice returned for
// each Match function. Keyed by aspect name.
type fakeAspectFn func(language string, sigPatterns []string, dstFilePrefix, dstNameLike string) []query.AspectMatch

func (f *fakeReader) SymbolsBySignatureLike(ctx context.Context, language string, patterns []string, limit int) ([]query.AspectMatch, error) {
	if f.aspectFn == nil {
		return nil, nil
	}
	return f.aspectFn(language, patterns, "", ""), nil
}

func (f *fakeReader) SymbolsByOutboundRef(ctx context.Context, language, dstFilePrefix, dstNameLike string, limit int) ([]query.AspectMatch, error) {
	if f.aspectFn == nil {
		return nil, nil
	}
	return f.aspectFn(language, nil, dstFilePrefix, dstNameLike), nil
}

// TestCompile_GoldenSinglePackage exercises the lean SKILL.md path
// against a hand-built fixture: one Go package with two files, three
// top-level symbols, two inbound callers, one outbound callee. Asserts
// byte-equality with testdata/golden/single-package/packages/internal/query/SKILL.md.
func TestCompile_GoldenSinglePackage(t *testing.T) {
	t.Parallel()
	r := &fakeReader{
		files: []query.FileHit{
			{Path: "internal/query/query.go", Language: "go", SymbolCount: 5},
			{Path: "internal/query/summary.go", Language: "go", SymbolCount: 2},
		},
		summaries: map[string]query.FileSummary{
			"internal/query/query.go": {
				Path:        "internal/query/query.go",
				Language:    "go",
				SymbolCount: 5,
				Imports:     []string{"context", "database/sql"},
			},
			"internal/query/summary.go": {
				Path:        "internal/query/summary.go",
				Language:    "go",
				SymbolCount: 2,
				Imports:     []string{"context", "database/sql"},
			},
		},
		outlines: map[string][]query.FileOutlineItem{
			"internal/query/query.go": {
				{Name: "Reader", Qualified: "query.Reader", Kind: "type", StartLine: 15, Signature: "type Reader struct{ db *sql.DB }"},
				{Name: "NewReader", Qualified: "query.NewReader", Kind: "function", StartLine: 19, Signature: "func NewReader(db *sql.DB) *Reader"},
				{Name: "FindSymbol", Qualified: "query.Reader.FindSymbol", Kind: "function", StartLine: 45, Signature: "func (r *Reader) FindSymbol(ctx context.Context, name string) ([]SymbolHit, error)"},
			},
			"internal/query/summary.go": {
				{Name: "FileSummary", Qualified: "query.FileSummary", Kind: "type", StartLine: 8, Signature: "type FileSummary struct{ Path string }"},
				{Name: "GetFileSummary", Qualified: "query.Reader.GetFileSummary", Kind: "function", StartLine: 34, Signature: "func (r *Reader) GetFileSummary(ctx context.Context, p string) (FileSummary, error)"},
			},
		},
		inbound: map[string][]query.PackageRefAgg{
			"internal/query": {
				{Path: "internal/daemon", RefCount: 78},
				{Path: "cmd/myco", RefCount: 22},
			},
		},
		outbound: map[string][]query.PackageRefAgg{
			"internal/query": {
				{Path: "internal/index", RefCount: 12},
			},
		},
	}

	out := t.TempDir()
	now, _ := time.Parse(time.RFC3339, "2026-04-26T12:00:00Z")
	if err := Compile(context.Background(), r, Options{
		OutDir:      out,
		Now:         now,
		TopRefLimit: 20,
	}); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	assertGolden(t, out, filepath.Join("testdata", "golden", "single-package"), []string{
		"packages/internal/query/SKILL.md",
		"INDEX.md",
	})
}

// TestCompile_GoldenMultiLanguage covers the directory unification
// rule: when a directory contains files of more than one language,
// one SKILL.md is emitted with language=mixed and every file listed
// regardless of language. The fixture has a Go file, a TS file, and a
// Python file all in src/.
func TestCompile_GoldenMultiLanguage(t *testing.T) {
	t.Parallel()
	r := &fakeReader{
		files: []query.FileHit{
			{Path: "src/main.go", Language: "go"},
			{Path: "src/api.ts", Language: "typescript"},
			{Path: "src/util.py", Language: "python"},
		},
		summaries: map[string]query.FileSummary{
			"src/main.go":  {Path: "src/main.go", Language: "go", SymbolCount: 1},
			"src/api.ts":   {Path: "src/api.ts", Language: "typescript", SymbolCount: 1},
			"src/util.py":  {Path: "src/util.py", Language: "python", SymbolCount: 1},
		},
		outlines: map[string][]query.FileOutlineItem{
			"src/main.go": {
				{Name: "main", Qualified: "main.main", Kind: "function", StartLine: 5, Signature: "func main()"},
			},
			"src/api.ts": {
				{Name: "handle", Qualified: "api.handle", Kind: "function", StartLine: 1, Signature: "function handle(req: Request): Response"},
			},
			"src/util.py": {
				{Name: "fmt", Qualified: "util.fmt", Kind: "function", StartLine: 1, Signature: "def fmt(x): ..."},
			},
		},
		inbound:  map[string][]query.PackageRefAgg{},
		outbound: map[string][]query.PackageRefAgg{},
	}
	out := t.TempDir()
	now, _ := time.Parse(time.RFC3339, "2026-04-26T12:00:00Z")
	if err := Compile(context.Background(), r, Options{
		OutDir: out,
		Now:    now,
	}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	assertGolden(t, out, filepath.Join("testdata", "golden", "multi-language"), []string{
		"packages/src/SKILL.md",
		"INDEX.md",
	})
}

// TestCompile_GoldenAspects exercises the aspects subtree:
// error-handling matches a Go signature returning error,
// context-propagation matches one taking context.Context,
// config-loading and logging are heuristic — fixture returns 1 hit
// each. Asserts every emitted aspect INDEX.md plus the root index
// (which now lists aspects in a second table).
func TestCompile_GoldenAspects(t *testing.T) {
	t.Parallel()
	r := &fakeReader{
		files: []query.FileHit{
			{Path: "internal/query/query.go", Language: "go"},
		},
		summaries: map[string]query.FileSummary{
			"internal/query/query.go": {Path: "internal/query/query.go", Language: "go", SymbolCount: 1},
		},
		outlines: map[string][]query.FileOutlineItem{
			"internal/query/query.go": {
				{Name: "FindSymbol", Qualified: "query.Reader.FindSymbol", Kind: "function", StartLine: 45,
					Signature: "func (r *Reader) FindSymbol(ctx context.Context, name string) ([]SymbolHit, error)"},
			},
		},
		aspectFn: func(language string, sigPatterns []string, dstFilePrefix, dstNameLike string) []query.AspectMatch {
			// Branch on the filter type: signature patterns ->
			// clean aspect; outbound prefix/like -> heuristic.
			match := query.AspectMatch{
				SymbolID: 1, Name: "FindSymbol",
				Qualified: "query.Reader.FindSymbol",
				Kind:      "function",
				Path:      "internal/query/query.go",
				StartLine: 45,
				Signature: "func (r *Reader) FindSymbol(ctx context.Context, name string) ([]SymbolHit, error)",
			}
			switch {
			case len(sigPatterns) > 0:
				match.InboundRefs = 12
				return []query.AspectMatch{match}
			case dstFilePrefix == "internal/config":
				match.Qualified = "config.Load"
				match.Name = "Load"
				match.InboundRefs = 4
				match.Signature = "func Load(path string) (Config, error)"
				return []query.AspectMatch{match}
			case dstNameLike == "log.%":
				match.Qualified = "daemon.stderrLogger.Printf"
				match.Name = "Printf"
				match.InboundRefs = 1
				match.Signature = "func (stderrLogger) Printf(format string, args ...any)"
				return []query.AspectMatch{match}
			}
			return nil
		},
	}
	out := t.TempDir()
	now, _ := time.Parse(time.RFC3339, "2026-04-26T12:00:00Z")
	if err := Compile(context.Background(), r, Options{
		OutDir: out,
		Now:    now,
	}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	assertGolden(t, out, filepath.Join("testdata", "golden", "aspects"), []string{
		"INDEX.md",
		"aspects/error-handling/INDEX.md",
		"aspects/context-propagation/INDEX.md",
		"aspects/config-loading/INDEX.md",
		"aspects/logging/INDEX.md",
	})
}

// assertGolden compares each relPath under genRoot against the
// corresponding file under goldenDir. With the package's -update flag
// set, golden files are (re)written instead of compared. New goldens
// are created with their parent directories.
func assertGolden(t *testing.T, genRoot, goldenDir string, relPaths []string) {
	t.Helper()
	for _, rel := range relPaths {
		got := mustReadFile(t, filepath.Join(genRoot, filepath.FromSlash(rel)))
		want := filepath.Join(goldenDir, filepath.FromSlash(rel))
		if *updateGolden {
			if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", filepath.Dir(want), err)
			}
			if err := os.WriteFile(want, []byte(got), 0o644); err != nil {
				t.Fatalf("write %s: %v", want, err)
			}
			t.Logf("updated %s", want)
			continue
		}
		wantContents := mustReadFile(t, want)
		if got != wantContents {
			t.Fatalf("%s mismatch.\n--- got ---\n%s\n--- want ---\n%s", rel, got, wantContents)
		}
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
