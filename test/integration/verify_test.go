// Integration tests for verify_changes (`myco check`): a symbol removed
// in the working tree that files outside the change set still reference
// must FAIL with the call site named; clean deletions pass; a stale
// index fails the freshness gate instead of vouching for broken code.
package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/repo"
	goresolver "github.com/datata1/mycelium/internal/resolver/golang"
	"github.com/datata1/mycelium/internal/service"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// verifyFixture builds a committed, indexed Go repo where b.go calls
// a.go's Widget and returns the service wired like the daemon wires it.
func verifyFixture(t *testing.T) (dir string, svc *service.Service, p *pipeline.Pipeline, ctx context.Context) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()

	writeFile(t, dir, "go.mod", "module example.com/demo\n\ngo 1.22\n")
	writeFile(t, dir, "a.go", `package demo

func Widget() string { return "w" }

func Orphan() string { return "o" }
`)
	writeFile(t, dir, "b.go", `package demo

func Build() string {
	return Widget()
}
`)
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "test@example.com")
	gitIn(t, dir, "config", "user.name", "Test")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "baseline")

	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	ctx = c

	ix := openIndex(t, filepath.Join(dir, ".mycelium", "index.db"))
	t.Cleanup(func() { ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(golang.New())
	gr := goresolver.New(dir)
	if _, err := gr.Load(); err != nil {
		t.Fatalf("resolver load: %v", err)
	}

	walker := repo.NewWalker(dir, []string{"**/*.go"}, nil, 0)
	p = &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   walker,
		Resolvers: map[string]pipeline.Resolver{
			"go": gr,
		},
	}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}

	svc = newVerifyService(t, ix, dir, reg)
	return dir, svc, p, ctx
}

func newVerifyService(t *testing.T, ix *index.Index, root string, reg *parser.Registry) *service.Service {
	t.Helper()
	svc := service.NewReadOnly(ix, root, nil)
	svc.SetParsers(reg)
	return svc
}

func checkLevel(t *testing.T, rep ipc.VerifyReport, name string) string {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c.Level
		}
	}
	t.Fatalf("check %q missing in %+v", name, rep.Checks)
	return ""
}

func TestIntegration_VerifyChanges(t *testing.T) {
	t.Parallel()
	dir, svc, p, ctx := verifyFixture(t)

	t.Run("clean_tree_passes", func(t *testing.T) {
		rep, err := svc.VerifyChanges(ctx, ipc.VerifyChangesParams{})
		if err != nil {
			t.Fatalf("VerifyChanges: %v", err)
		}
		if rep.ExitCode() != 0 || rep.ChangedFiles != 0 {
			t.Errorf("clean tree: exit=%d changed=%d, want 0/0 (%+v)", rep.ExitCode(), rep.ChangedFiles, rep.Checks)
		}
	})

	// Remove Widget (b.go still calls it), reindex so the index is
	// fresh — the removal check must FAIL and name b.go.
	t.Run("removed_but_referenced_fails", func(t *testing.T) {
		writeFile(t, dir, "a.go", `package demo

func Orphan() string { return "o" }
`)
		if _, err := p.RunOnce(ctx); err != nil {
			t.Fatalf("reindex: %v", err)
		}
		rep, err := svc.VerifyChanges(ctx, ipc.VerifyChangesParams{})
		if err != nil {
			t.Fatalf("VerifyChanges: %v", err)
		}
		if got := checkLevel(t, rep, "removed_but_referenced"); got != "fail" {
			t.Fatalf("removed_but_referenced = %s, want fail (%+v)", got, rep)
		}
		if rep.ExitCode() != 2 {
			t.Errorf("exit = %d, want 2", rep.ExitCode())
		}
		var found bool
		for _, rm := range rep.Removed {
			if rm.Qualified != "demo.Widget" {
				continue
			}
			for _, d := range rm.Danglers {
				if strings.Contains(d.Path, "b.go") {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("expected dangler in b.go for demo.Widget; got %+v", rep.Removed)
		}
	})

	// Restore Widget, remove the unreferenced Orphan instead: a clean
	// deletion must pass.
	t.Run("clean_deletion_passes", func(t *testing.T) {
		writeFile(t, dir, "a.go", `package demo

func Widget() string { return "w" }
`)
		if _, err := p.RunOnce(ctx); err != nil {
			t.Fatalf("reindex: %v", err)
		}
		rep, err := svc.VerifyChanges(ctx, ipc.VerifyChangesParams{})
		if err != nil {
			t.Fatalf("VerifyChanges: %v", err)
		}
		if got := checkLevel(t, rep, "removed_but_referenced"); got != "pass" {
			t.Fatalf("removed_but_referenced = %s, want pass (%+v)", got, rep)
		}
		var msg string
		for _, c := range rep.Checks {
			if c.Name == "removed_but_referenced" {
				msg = c.Message
			}
		}
		if !strings.Contains(msg, "removed cleanly") {
			t.Errorf("expected clean-deletion message, got %q", msg)
		}
	})

	// Edit without reindexing: the freshness gate must fail rather than
	// judge from stale rows.
	t.Run("stale_index_fails_freshness", func(t *testing.T) {
		writeFile(t, dir, "a.go", `package demo

func Widget() string { return "w2" }
`)
		// Ensure the mtime moves past second-granularity noise.
		future := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(filepath.Join(dir, "a.go"), future, future); err != nil {
			t.Fatal(err)
		}
		rep, err := svc.VerifyChanges(ctx, ipc.VerifyChangesParams{})
		if err != nil {
			t.Fatalf("VerifyChanges: %v", err)
		}
		if got := checkLevel(t, rep, "index_fresh_for_changes"); got != "fail" {
			t.Errorf("index_fresh_for_changes = %s, want fail (%+v)", got, rep.Checks)
		}
		// Re-sync for any later subtests.
		if _, err := p.RunOnce(ctx); err != nil {
			t.Fatalf("reindex: %v", err)
		}
	})

	// A brand-new untracked file cannot remove symbols and must not
	// affect the verdict.
	t.Run("untracked_file_invisible", func(t *testing.T) {
		writeFile(t, dir, "new.go", `package demo

func Fresh() {}
`)
		t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "new.go")) })
		if _, err := p.RunOnce(ctx); err != nil {
			t.Fatalf("reindex: %v", err)
		}
		rep, err := svc.VerifyChanges(ctx, ipc.VerifyChangesParams{})
		if err != nil {
			t.Fatalf("VerifyChanges: %v", err)
		}
		if got := checkLevel(t, rep, "removed_but_referenced"); got != "pass" {
			t.Errorf("removed_but_referenced = %s, want pass (%+v)", got, rep)
		}
	})
}

// Changing a.go must select exactly the test file that reaches the
// changed code through the call graph (b_test.go → Build → Widget),
// never the unrelated c_test.go.
func TestIntegration_SelectTests(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()

	writeFile(t, dir, "go.mod", "module example.com/demo\n\ngo 1.22\n")
	writeFile(t, dir, "a.go", `package demo

func Widget() string { return "w" }
`)
	writeFile(t, dir, "b.go", `package demo

func Build() string {
	return Widget()
}
`)
	writeFile(t, dir, "b_test.go", `package demo

import "testing"

func TestBuild(t *testing.T) {
	if Build() == "" {
		t.Fatal("empty")
	}
}
`)
	writeFile(t, dir, "c.go", `package demo

func Unrelated() int { return 1 }
`)
	writeFile(t, dir, "c_test.go", `package demo

import "testing"

func TestUnrelated(t *testing.T) {
	if Unrelated() != 1 {
		t.Fatal("nope")
	}
}
`)
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "test@example.com")
	gitIn(t, dir, "config", "user.name", "Test")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "baseline")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dir, ".mycelium", "index.db"))
	t.Cleanup(func() { ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(golang.New())
	gr := goresolver.New(dir)
	if _, err := gr.Load(); err != nil {
		t.Fatalf("resolver load: %v", err)
	}

	walker := repo.NewWalker(dir, []string{"**/*.go"}, nil, 0)
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   walker,
		Resolvers: map[string]pipeline.Resolver{
			"go": gr,
		},
	}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}
	svc := newVerifyService(t, ix, dir, reg)

	// Edit a.go (Widget's body) and reindex.
	writeFile(t, dir, "a.go", `package demo

func Widget() string { return "w2" }
`)
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	res, err := svc.SelectTests(ctx, ipc.SelectTestsParams{})
	if err != nil {
		t.Fatalf("SelectTests: %v", err)
	}
	var gotB, gotC bool
	for _, tf := range res.TestFiles {
		switch tf.Path {
		case "b_test.go":
			gotB = true
		case "c_test.go":
			gotC = true
		}
	}
	if !gotB {
		t.Errorf("expected b_test.go in selection; got %+v", res.TestFiles)
	}
	if gotC {
		t.Errorf("c_test.go must NOT be selected; got %+v", res.TestFiles)
	}

	// A changed test file itself ranks at distance 0.
	writeFile(t, dir, "c_test.go", `package demo

import "testing"

func TestUnrelated(t *testing.T) {
	if Unrelated() != 1 {
		t.Fatal("changed")
	}
}
`)
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	res, err = svc.SelectTests(ctx, ipc.SelectTestsParams{})
	if err != nil {
		t.Fatalf("SelectTests: %v", err)
	}
	var cDist = -1
	for _, tf := range res.TestFiles {
		if tf.Path == "c_test.go" {
			cDist = tf.Distance
		}
	}
	if cDist != 0 {
		t.Errorf("changed c_test.go should be selected at distance 0; got %d (%+v)", cDist, res.TestFiles)
	}
}
