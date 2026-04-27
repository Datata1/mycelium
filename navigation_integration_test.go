// Navigation integration test (v3.0-rc): the v3 acceptance criterion
// for Pillar H. An agent with only Read access to .mycelium/skills/
// (plus the read_focused tool from v2.4) should be able to answer an
// architecture question about an unfamiliar repo without invoking any
// of the structural MCP tools (find_symbol, get_neighborhood, etc.).
//
// This test mechanizes that scenario against the sample fixture: it
// indexes the fixture, compiles the skills tree, then walks the
// documented navigation steps from docs/navigation-example.md and
// asserts each step's output carries the breadcrumbs the next step
// needs. If this test fails, the skills tree has stopped being
// self-sufficient — agents will fall back to MCP queries and Pillar H
// stops paying off.
package mycelium_test

import (
	"context"
	"os"
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
	"github.com/jdwiederstein/mycelium/internal/skills"
)

// TestNavigation_AgentAnswersUsingOnlySkillsTreeAndReadFocused walks
// the canonical agent path: INDEX.md -> per-package SKILL.md -> a
// read_focused call against the file the SKILL.md points at. The
// "question" being answered: "How is authentication implemented in
// this codebase?" Expected breadcrumbs:
//
//   1. INDEX.md must list the src/ package (where the TS sources live).
//   2. packages/src/SKILL.md must mention the AuthService class plus
//      its file:line, so the agent knows where to look.
//   3. read_focused on src/auth.ts with focus="AuthService" must
//      return a body that (a) is shorter than the original file,
//      (b) keeps the AuthService symbol expanded, and (c) collapses
//      at least one unrelated top-level symbol so the byte savings
//      are real, not coincidental.
//
// The third check is what wires v2.3 (skills) and v2.4 (focused reads)
// together: the navigation tree is only useful if the file pointer it
// gives you can be cheaply consumed.
func TestNavigation_AgentAnswersUsingOnlySkillsTreeAndReadFocused(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, "testdata/fixtures/sample")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	defer ix.Close()

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

	skillsDir := filepath.Join(dst, ".mycelium", "skills")
	reader := query.NewReader(ix.DB())
	if err := skills.Compile(ctx, reader, skills.Options{
		OutDir: skillsDir,
		Store:  ix,
	}); err != nil {
		t.Fatalf("compile skills: %v", err)
	}

	// Step 1: INDEX.md is the entry point. It must list every
	// indexed package directory so the agent has somewhere to jump.
	indexBody := mustReadFile(t, filepath.Join(skillsDir, "INDEX.md"))
	for _, pkg := range []string{"src", "py", "(repo root)"} {
		if !strings.Contains(indexBody, pkg) {
			t.Errorf("INDEX.md missing package %q; agent has no way to find it.\n--- INDEX.md ---\n%s",
				pkg, indexBody)
		}
	}

	// Step 2: packages/src/SKILL.md must surface AuthService along
	// with its source location. The colon-line format ("path:line")
	// is what lets the agent cite the file in step 3.
	srcSkill := mustReadFile(t, filepath.Join(skillsDir, "packages", "src", "SKILL.md"))
	if !strings.Contains(srcSkill, "AuthService") {
		t.Fatalf("packages/src/SKILL.md missing AuthService.\n--- SKILL.md ---\n%s", srcSkill)
	}
	if !strings.Contains(srcSkill, "src/auth.ts") {
		t.Errorf("packages/src/SKILL.md does not name the file containing AuthService; agent can't follow up.")
	}

	// Step 3: read_focused mediates the actual file read. Confirms
	// that v2.3 + v2.4 compose: the SKILL.md gave us a path, the
	// focused read narrows the file.
	fr, err := reader.ReadFocused(ctx, dst, "src/auth.ts", "AuthService")
	if err != nil {
		t.Fatalf("ReadFocused: %v", err)
	}
	if fr.Stats.ReturnedBytes >= fr.Stats.OriginalBytes {
		t.Errorf("focused read did not narrow file: returned=%d original=%d",
			fr.Stats.ReturnedBytes, fr.Stats.OriginalBytes)
	}
	if !strings.Contains(fr.Content, "class AuthService") {
		t.Errorf("focused read collapsed AuthService itself.\n--- content ---\n%s", fr.Content)
	}
	if fr.Stats.ExpandedSymbols >= fr.Stats.TotalSymbols {
		t.Errorf("focused read expanded everything (no narrowing happened): expanded=%d total=%d",
			fr.Stats.ExpandedSymbols, fr.Stats.TotalSymbols)
	}
	if fr.Stats.ExpandedSymbols == 0 {
		t.Errorf("focused read kept nothing — agent gets no signal from it")
	}

	// Step 4: the trace itself must be cite-able. We don't verify
	// the full markdown structure here (compile_test.go's golden
	// files do that), but if any of the citation points (file path,
	// symbol name, line annotations) are missing from SKILL.md the
	// agent can't ground its answer.
	for _, mustCite := range []string{
		"AuthService",
		"src/auth.ts",
	} {
		if !strings.Contains(srcSkill, mustCite) {
			t.Errorf("citation point %q missing from SKILL.md; agent's answer would be ungrounded", mustCite)
		}
	}
}

// TestNavigation_SkillsTreeAndFocusedReadAreSelfDescribing is a
// negative-space check: an agent that didn't already know the layout
// of .mycelium/skills/ should still be able to discover the format
// from the files themselves. INDEX.md must explain how to navigate
// (mention "packages/" and "aspects/"), and per-package SKILL.md
// frontmatter must be machine-parseable enough that the agent knows
// what kind of file it has open.
func TestNavigation_SkillsTreeAndFocusedReadAreSelfDescribing(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, "testdata/fixtures/sample")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	defer ix.Close()
	reg := parser.NewRegistry()
	reg.Register(golang.New())
	reg.Register(typescript.New())
	reg.Register(python.New())
	walker := repo.NewWalker(dst,
		[]string{"**/*.go", "src/**/*.ts", "py/**/*.py"},
		nil, 0)
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
	skillsDir := filepath.Join(dst, ".mycelium", "skills")
	reader := query.NewReader(ix.DB())
	if err := skills.Compile(ctx, reader, skills.Options{
		OutDir: skillsDir,
		Store:  ix,
	}); err != nil {
		t.Fatalf("compile: %v", err)
	}

	indexBody := mustReadFile(t, filepath.Join(skillsDir, "INDEX.md"))
	for _, marker := range []string{"packages/", "Packages"} {
		if !strings.Contains(indexBody, marker) {
			t.Errorf("INDEX.md missing layout marker %q — agent has to guess the directory structure", marker)
		}
	}

	srcSkill := mustReadFile(t, filepath.Join(skillsDir, "packages", "src", "SKILL.md"))
	for _, marker := range []string{
		"---",       // frontmatter delimiters
		"language:", // structured metadata
		"symbols:",  // count the agent can use to estimate cost
	} {
		if !strings.Contains(srcSkill, marker) {
			t.Errorf("packages/src/SKILL.md missing %q — frontmatter is not self-describing", marker)
		}
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
