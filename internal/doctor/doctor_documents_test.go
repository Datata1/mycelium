package doctor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdwiederstein/mycelium/internal/query"
)

// insertDocumentFile registers a file under a non-NULL document_kind
// so the v3.3 doctor check sees it as a candidate.
func insertDocumentFile(t *testing.T, db *sql.DB, path, kind string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO files(path, language, size_bytes, mtime_ns, content_hash, parse_hash, last_indexed_at, project_id, document_kind)
		 VALUES(?, '', 0, 0, x'00', x'00', 0, NULL, ?)`, path, kind)
	if err != nil {
		t.Fatalf("insert document file: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// insertDocumentEntry adds one (key, value) row to the documents
// table. Used to drive doctor coverage states explicitly.
func insertDocumentEntry(t *testing.T, db *sql.DB, fileID int64, kind, key, value string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO documents(file_id, kind, key, value, line) VALUES(?, ?, ?, ?, ?)`,
		fileID, kind, key, value, 1,
	); err != nil {
		t.Fatalf("insert document entry: %v", err)
	}
}

// TestDocumentsCheck_SkippedOnCodeOnlyRepo: a fresh repo with no
// document files at all should not emit a documents_indexed check.
func TestDocumentsCheck_SkippedOnCodeOnlyRepo(t *testing.T) {
	ix := openTestIndex(t)
	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c := findCheck(rep, "documents_indexed"); c != nil {
		t.Fatalf("expected check to be skipped on code-only repo; got %+v", c)
	}
}

// TestDocumentsCheck_PassWithEntries: a file plus matching entries
// produces a pass-level check listing the per-kind count.
func TestDocumentsCheck_PassWithEntries(t *testing.T) {
	ix := openTestIndex(t)
	fid := insertDocumentFile(t, ix.DB(), "locales/en.json", "i18n_json")
	insertDocumentEntry(t, ix.DB(), fid, "i18n_json", "topbar.back", "Go back")
	insertDocumentEntry(t, ix.DB(), fid, "i18n_json", "topbar.fwd", "Go forward")

	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := findCheck(rep, "documents_indexed")
	if c == nil {
		t.Fatalf("expected documents_indexed check")
	}
	if c.Level != LevelPass {
		t.Errorf("level: got %q, want pass; msg=%q", c.Level, c.Message)
	}
	if !strings.Contains(c.Message, "i18n_json:2") {
		t.Errorf("expected per-kind count in message; got %q", c.Message)
	}
}

// TestTelemetryDarkSpot_FlagsWhenSessionsButNoTelemetry pins the v3.4 G5
// finding: when session hooks are recording fallback tools but
// telemetry.jsonl is empty/absent, the agent is "flying blind on
// adoption metrics" and doctor surfaces a warn.
func TestTelemetryDarkSpot_FlagsWhenSessionsButNoTelemetry(t *testing.T) {
	repoRoot := t.TempDir()
	mDir := filepath.Join(repoRoot, ".mycelium")
	if err := os.MkdirAll(mDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Drop a fake session-external file so the dark-spot detector
	// sees recorded sessions.
	if err := os.WriteFile(
		filepath.Join(mDir, "session_ses_test_external.jsonl"),
		[]byte(`{"tool":"Read","category":"exploratory"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	// Deliberately do not create telemetry.jsonl.

	ix := openTestIndex(t)
	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), repoRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := findCheck(rep, "telemetry_dark_spot")
	if c == nil {
		t.Fatalf("expected telemetry_dark_spot check")
	}
	if c.Level != LevelWarn {
		t.Errorf("level: got %q, want warn", c.Level)
	}
	if !strings.Contains(c.Message, "telemetry.jsonl is empty/missing") {
		t.Errorf("expected dark-spot headline; got %q", c.Message)
	}
}

// TestTelemetryDarkSpot_QuietWhenBothPresent: telemetry is on (file
// has bytes) → no warn, dark-spot check stays silent.
func TestTelemetryDarkSpot_QuietWhenBothPresent(t *testing.T) {
	repoRoot := t.TempDir()
	mDir := filepath.Join(repoRoot, ".mycelium")
	if err := os.MkdirAll(mDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(mDir, "session_ses_test_external.jsonl"),
		[]byte(`{"tool":"Read","category":"exploratory"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(mDir, "telemetry.jsonl"),
		[]byte(`{"tool":"find_symbol","ok":true}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write telemetry: %v", err)
	}
	ix := openTestIndex(t)
	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), repoRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c := findCheck(rep, "telemetry_dark_spot"); c != nil {
		t.Errorf("expected no warn when telemetry is on; got %+v", c)
	}
}

// TestTelemetryDarkSpot_QuietOnFreshRepo: no session traffic yet, no
// telemetry — dark-spot check stays silent (we don't pre-emptively
// nag a quiet repo).
func TestTelemetryDarkSpot_QuietOnFreshRepo(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".mycelium"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ix := openTestIndex(t)
	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), repoRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c := findCheck(rep, "telemetry_dark_spot"); c != nil {
		t.Errorf("fresh repo should not trigger dark-spot warn; got %+v", c)
	}
}

// TestDocumentsCheck_WarnsOnEmptyDocumentFile: a file registered with
// document_kind but no rows in the documents table is the diagnostic
// case worth flagging. Doctor surfaces it as a warn-level check.
func TestDocumentsCheck_WarnsOnEmptyDocumentFile(t *testing.T) {
	ix := openTestIndex(t)
	_ = insertDocumentFile(t, ix.DB(), "locales/broken.json", "i18n_json")
	// Intentionally no entries — simulates a parser claim with zero
	// extracted entries.

	r := query.NewReader(ix.DB())
	rep, err := Run(context.Background(), r, "none", DefaultThresholds(), "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := findCheck(rep, "documents_indexed")
	if c == nil {
		t.Fatalf("expected documents_indexed check")
	}
	if c.Level != LevelWarn {
		t.Errorf("level: got %q, want warn; msg=%q", c.Level, c.Message)
	}
	if !strings.Contains(c.Message, "0 entries") {
		t.Errorf("expected '0 entries' hint in message; got %q", c.Message)
	}
}
