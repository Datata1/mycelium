package doctor

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/query"
)

// findCheck returns the first check with the given name, or nil. Doctor
// reports are unordered for callers, so direct slice indexing isn't
// stable across check additions.
func findCheck(rep Report, name string) *Check {
	for i := range rep.Checks {
		if rep.Checks[i].Name == name {
			return &rep.Checks[i]
		}
	}
	return nil
}

// insertProject is a test helper — bypasses the pipeline so the test
// stays focused on the doctor surface.
func insertProject(t *testing.T, db *sql.DB, name, root string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO projects(name, root, created_at) VALUES(?, ?, ?)`, name, root, 0)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// insertFile attaches a file to a project (or NULL for the root project).
func insertFile(t *testing.T, db *sql.DB, path string, projectID *int64) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO files(path, language, size_bytes, mtime_ns, content_hash, parse_hash, last_indexed_at, project_id)
		 VALUES(?, ?, 0, 0, x'00', x'00', 0, ?)`, path, "go", projectID)
	if err != nil {
		t.Fatalf("insert file: %v", err)
	}
}

func openTestIndex(t *testing.T) *index.Index {
	t.Helper()
	dir := t.TempDir()
	ix, err := index.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	// Doctor's corpus_present check fails fast on an empty index, so
	// every test inserts at least one file under the root project
	// (project_id = NULL) before configuring projects.
	insertFile(t, ix.DB(), "ROOT.go", nil)
	return ix
}

// TestProjectsCheck_SkippedWhenSingleProject covers the default case:
// no `projects:` block in .mycelium.yml, project_id is NULL on every
// row, and the doctor should not emit a projects_configured_but_empty
// check at all.
func TestProjectsCheck_SkippedWhenSingleProject(t *testing.T) {
	ix := openTestIndex(t)
	r := query.NewReader(ix.DB())

	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c := findCheck(rep, "projects_configured_but_empty"); c != nil {
		t.Fatalf("expected check to be skipped, got %+v", c)
	}
}

// TestProjectsCheck_PassWhenAllPopulated: two configured projects, both
// well-populated, should pass.
func TestProjectsCheck_PassWhenAllPopulated(t *testing.T) {
	ix := openTestIndex(t)
	pidA := insertProject(t, ix.DB(), "frontend", "packages/frontend")
	pidB := insertProject(t, ix.DB(), "backend", "packages/backend")
	for i := 0; i < 15; i++ {
		insertFile(t, ix.DB(), filepath.Join("packages/frontend", "a.go"+string(rune('0'+i%10))+string(rune('0'+i/10))), &pidA)
	}
	for i := 0; i < 12; i++ {
		insertFile(t, ix.DB(), filepath.Join("packages/backend", "b.go"+string(rune('0'+i%10))+string(rune('0'+i/10))), &pidB)
	}

	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := findCheck(rep, "projects_configured_but_empty")
	if c == nil {
		t.Fatalf("expected projects check to be present")
	}
	if c.Level != LevelPass {
		t.Fatalf("expected pass, got %s: %s", c.Level, c.Message)
	}
}

// TestProjectsCheck_FailsOnEmptyProject: when an include pattern matches
// nothing, the project row exists but has zero files. This is the bug
// shape the field test surfaced — silent zero results that look like a
// real miss.
func TestProjectsCheck_FailsOnEmptyProject(t *testing.T) {
	ix := openTestIndex(t)
	pidGood := insertProject(t, ix.DB(), "frontend", "packages/frontend")
	insertProject(t, ix.DB(), "ide", "packages/ide") // empty — the bug
	for i := 0; i < 15; i++ {
		insertFile(t, ix.DB(), filepath.Join("packages/frontend", "a.go"+string(rune('0'+i%10))+string(rune('0'+i/10))), &pidGood)
	}

	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := findCheck(rep, "projects_configured_but_empty")
	if c == nil {
		t.Fatalf("expected projects check to be present")
	}
	if c.Level != LevelFail {
		t.Fatalf("expected fail, got %s: %s", c.Level, c.Message)
	}
	fails, _ := c.Detail["fails"].([]string)
	if len(fails) != 1 {
		t.Fatalf("expected 1 fail entry, got %v", fails)
	}
}

// TestProjectsCheck_WarnsOnTinyProject: a project with a small handful
// of files isn't impossible (small monorepo packages exist), but it's
// suspicious enough to warn.
func TestProjectsCheck_WarnsOnTinyProject(t *testing.T) {
	ix := openTestIndex(t)
	pidGood := insertProject(t, ix.DB(), "frontend", "packages/frontend")
	pidTiny := insertProject(t, ix.DB(), "tiny", "packages/tiny")
	for i := 0; i < 15; i++ {
		insertFile(t, ix.DB(), filepath.Join("packages/frontend", "a.go"+string(rune('0'+i%10))+string(rune('0'+i/10))), &pidGood)
	}
	for i := 0; i < 3; i++ {
		insertFile(t, ix.DB(), filepath.Join("packages/tiny", "t.go"+string(rune('0'+i))), &pidTiny)
	}

	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := findCheck(rep, "projects_configured_but_empty")
	if c == nil {
		t.Fatalf("expected projects check to be present")
	}
	if c.Level != LevelWarn {
		t.Fatalf("expected warn, got %s: %s", c.Level, c.Message)
	}
}
