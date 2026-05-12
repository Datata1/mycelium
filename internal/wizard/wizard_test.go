package wizard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdwiederstein/mycelium/internal/wizard"
)

// ── DetectLanguages ──────────────────────────────────────────────────────────

func TestDetectLanguages_Empty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	langs, err := wizard.DetectLanguages(dir)
	if err != nil {
		t.Fatalf("DetectLanguages: %v", err)
	}
	if len(langs) != 0 {
		t.Errorf("expected 0 langs, got %v", langs)
	}
}

func TestDetectLanguages_Mixed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "main.go", "package main")
	write(t, dir, "app.ts", "export {}")
	write(t, dir, "script.py", "pass")
	write(t, dir, "README.md", "# readme") // should be ignored

	langs, err := wizard.DetectLanguages(dir)
	if err != nil {
		t.Fatalf("DetectLanguages: %v", err)
	}
	byName := map[string]int{}
	for _, l := range langs {
		byName[l.Language] = l.Count
	}
	if byName["go"] != 1 {
		t.Errorf("go count = %d, want 1", byName["go"])
	}
	if byName["typescript"] != 1 {
		t.Errorf("typescript count = %d, want 1", byName["typescript"])
	}
	if byName["python"] != 1 {
		t.Errorf("python count = %d, want 1", byName["python"])
	}
}

func TestDetectLanguages_SkipsNodeModules(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "node_modules/lib/index.ts", "export {}")
	write(t, dir, "src/index.ts", "export {}")

	langs, err := wizard.DetectLanguages(dir)
	if err != nil {
		t.Fatalf("DetectLanguages: %v", err)
	}
	byName := map[string]int{}
	for _, l := range langs {
		byName[l.Language] = l.Count
	}
	// Only src/index.ts, not node_modules one.
	if byName["typescript"] != 1 {
		t.Errorf("typescript count = %d, want 1 (node_modules should be skipped)", byName["typescript"])
	}
}

// ── DetectSubprojects ────────────────────────────────────────────────────────

func TestSuggestProjectName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		relDir string
		want   string
	}{
		{"services/api", "api"},              // non-generic → keep as-is
		{"xxx-service/common", "xxx-service-common"}, // generic → parent-base
		{"xxx-service/node", "xxx-service-node"},     // generic → parent-base
		{"yyy-svc/shared", "yyy-svc-shared"},          // generic → parent-base
		{"packages/lib", "packages-lib"},     // generic → parent-base
		{"backend", "backend"},               // single component → as-is
		{"apps/dashboard", "dashboard"},      // non-generic → as-is
		{"services/worker", "worker"},        // non-generic → as-is
	}
	for _, tc := range cases {
		got := wizard.SuggestProjectName(tc.relDir)
		if got != tc.want {
			t.Errorf("SuggestProjectName(%q) = %q, want %q", tc.relDir, got, tc.want)
		}
	}
}

func TestDetectSubprojects_NoSubprojects(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/root\n\ngo 1.21")

	subs, err := wizard.DetectSubprojects(dir)
	if err != nil {
		t.Fatalf("DetectSubprojects: %v", err)
	}
	// Root go.mod must NOT be reported.
	if len(subs) != 0 {
		t.Errorf("expected 0 subprojects, got %v", subs)
	}
}

func TestDetectSubprojects_MonorepoGoMod(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module root")
	write(t, dir, "services/api/go.mod", "module api")
	write(t, dir, "services/worker/go.mod", "module worker")
	write(t, dir, "vendor/dep/go.mod", "module dep") // should be skipped

	subs, err := wizard.DetectSubprojects(dir)
	if err != nil {
		t.Fatalf("DetectSubprojects: %v", err)
	}
	if len(subs) != 2 {
		t.Errorf("expected 2 subprojects, got %d: %v", len(subs), subs)
	}
	dirs := map[string]bool{}
	for _, s := range subs {
		dirs[s.RelDir] = true
		if s.SuggestedName == "" {
			t.Errorf("SuggestedName empty for %s", s.RelDir)
		}
	}
	if !dirs["services/api"] {
		t.Error("expected services/api in subprojects")
	}
	if !dirs["services/worker"] {
		t.Error("expected services/worker in subprojects")
	}
}

func TestDetectSubprojects_MixedStack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "backend/go.mod", "module backend")
	write(t, dir, "frontend/package.json", `{"name":"frontend"}`)
	write(t, dir, "ml/pyproject.toml", "[project]")

	subs, err := wizard.DetectSubprojects(dir)
	if err != nil {
		t.Fatalf("DetectSubprojects: %v", err)
	}
	if len(subs) != 3 {
		t.Errorf("expected 3 subprojects, got %d", len(subs))
	}
}

// ── MCP JSON merge ────────────────────────────────────────────────────────────

func TestWriteClaudeCodeMCP_CreatesFresh(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")

	if err := wizard.WriteClaudeCodeMCP(path, "/usr/bin/myco", "/repo"); err != nil {
		t.Fatalf("WriteClaudeCodeMCP: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"mycelium"`) {
		t.Error("expected mycelium key in written JSON")
	}
	if !strings.Contains(string(b), `"/usr/bin/myco"`) {
		t.Error("expected binary path in written JSON")
	}
}

func TestWriteClaudeCodeMCP_PreservesExistingKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	// Pre-existing config with another server.
	os.WriteFile(path, []byte(`{"theme":"dark","mcpServers":{"other":{"command":"foo"}}}`), 0o644)

	if err := wizard.WriteClaudeCodeMCP(path, "/bin/myco", "/repo"); err != nil {
		t.Fatalf("WriteClaudeCodeMCP: %v", err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, `"other"`) {
		t.Error("existing mcpServer 'other' was lost")
	}
	if !strings.Contains(s, `"mycelium"`) {
		t.Error("mycelium entry missing")
	}
	if !strings.Contains(s, `"dark"`) {
		t.Error("theme key was lost")
	}
}

// ── CLAUDE.md snippet ────────────────────────────────────────────────────────

func TestAppendPrimingSnippet_Fresh(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	os.WriteFile(path, []byte("# Existing content\n"), 0o644)

	wrote, err := wizard.AppendPrimingSnippet(path)
	if err != nil {
		t.Fatalf("AppendPrimingSnippet: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "Existing content") {
		t.Error("existing content was lost")
	}
	if !strings.Contains(string(b), "myco") {
		t.Error("snippet not appended")
	}
}

func TestAppendPrimingSnippet_Idempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "CLAUDE.md")

	wrote, err := wizard.AppendPrimingSnippet(path)
	if err != nil || !wrote {
		t.Fatalf("first append failed: wrote=%v err=%v", wrote, err)
	}
	wrote, err = wizard.AppendPrimingSnippet(path)
	if err != nil {
		t.Fatalf("second append error: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false on second call (idempotent)")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func write(t *testing.T, base, rel, content string) {
	t.Helper()
	path := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
