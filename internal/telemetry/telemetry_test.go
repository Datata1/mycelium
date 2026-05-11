package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFileRecorder_AppendAndAggregate exercises the round-trip: open a
// fresh log, write several records, aggregate. Per-tool counts and the
// "all" rollup must agree with what we wrote, and concurrent writes
// from many goroutines must not corrupt the file.
func TestFileRecorder_AppendAndAggregate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")

	rec, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const callsPerTool = 20
	tools := []string{"find_symbol", "get_neighborhood", "stats"}
	var wg sync.WaitGroup
	for _, tool := range tools {
		for i := 0; i < callsPerTool; i++ {
			wg.Add(1)
			go func(tool string, i int) {
				defer wg.Done()
				if err := rec.Record(Record{
					Tool:        tool,
					InputBytes:  10 * (i + 1),
					OutputBytes: 100 * (i + 1),
					DurationMS:  int64(i + 1),
					OK:          i%5 != 0,
				}); err != nil {
					t.Errorf("record: %v", err)
				}
			}(tool, i)
		}
	}
	wg.Wait()
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	summaries, err := Aggregate(path)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}

	wantTotal := callsPerTool * len(tools)
	byTool := map[string]Summary{}
	for _, s := range summaries {
		byTool[s.Tool] = s
	}
	for _, tool := range tools {
		s, ok := byTool[tool]
		if !ok {
			t.Fatalf("missing summary for %q; got %v", tool, byTool)
		}
		if s.Count != callsPerTool {
			t.Errorf("tool %s: count = %d, want %d", tool, s.Count, callsPerTool)
		}
		if s.OK != callsPerTool*4/5 {
			t.Errorf("tool %s: ok = %d, want %d", tool, s.OK, callsPerTool*4/5)
		}
	}
	all, ok := byTool["all"]
	if !ok {
		t.Fatal("missing 'all' rollup")
	}
	if all.Count != wantTotal {
		t.Errorf("all.Count = %d, want %d", all.Count, wantTotal)
	}
	if all.MeanDuration <= 0 {
		t.Error("expected positive mean duration")
	}
	if all.P95Duration < all.P50Duration {
		t.Errorf("p95 (%v) < p50 (%v) — sort bug", all.P95Duration, all.P50Duration)
	}
}

// TestDisabled is a sanity check that the no-op recorder exists and
// satisfies the interface — important because the daemon will type-
// assert on Recorder, not the concrete type.
func TestDisabled(t *testing.T) {
	t.Parallel()
	var r Recorder = Disabled{}
	if err := r.Record(Record{Tool: "x", Timestamp: time.Now()}); err != nil {
		t.Errorf("Disabled.Record returned error: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Disabled.Close returned error: %v", err)
	}
}

// TestAggregate_MissingFile codifies the friendly behavior: telemetry
// configured but no calls yet (=> file doesn't exist) returns
// (nil, nil) so the CLI can render a hint instead of an error.
func TestAggregate_MissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nope.jsonl")
	summaries, err := Aggregate(path)
	if err != nil {
		t.Fatalf("Aggregate on missing file: %v", err)
	}
	if summaries != nil {
		t.Errorf("expected nil summaries for missing file; got %v", summaries)
	}
}

// TestSession_RoundTrip verifies: StartSession writes a marker, subsequent
// records carry the session ID, ListSessions returns the right call count,
// and AggregateSession scopes to that session only.
func TestSession_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "telemetry.jsonl")
	sessionFile := filepath.Join(dir, "current_session.json")

	// Start session A.
	metaA, err := StartSession(jsonl, sessionFile, "task-alpha")
	if err != nil {
		t.Fatalf("StartSession A: %v", err)
	}
	if !strings.HasPrefix(metaA.ID, "ses_") {
		t.Errorf("unexpected session ID format: %q", metaA.ID)
	}

	// Open recorder and point it at the session sidecar.
	rec, err := Open(jsonl)
	if err != nil {
		t.Fatalf("Open recorder: %v", err)
	}
	rec.SetSessionFile(sessionFile)

	// Write 3 calls in session A.
	for i := range 3 {
		_ = rec.Record(Record{Tool: "find_symbol", InputBytes: 10, OutputBytes: 100 * (i + 1), OK: true})
	}

	// Start session B — daemon picks it up on next Record().
	metaB, err := StartSession(jsonl, sessionFile, "task-beta")
	if err != nil {
		t.Fatalf("StartSession B: %v", err)
	}

	// Write 2 calls in session B.
	for range 2 {
		_ = rec.Record(Record{Tool: "search_lexical", InputBytes: 5, OutputBytes: 50, OK: true})
	}
	rec.Close()

	// ListSessions must return both, most recent first.
	reports, err := ListSessions(jsonl)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(reports))
	}
	if reports[0].Session.ID != metaB.ID {
		t.Errorf("expected most recent first: got %s want %s", reports[0].Session.ID, metaB.ID)
	}
	if reports[0].TotalCalls != 2 {
		t.Errorf("session B: want 2 calls, got %d", reports[0].TotalCalls)
	}
	if reports[1].TotalCalls != 3 {
		t.Errorf("session A: want 3 calls, got %d", reports[1].TotalCalls)
	}

	// AggregateSession must scope correctly.
	repA, err := AggregateSession(jsonl, metaA.ID)
	if err != nil {
		t.Fatalf("AggregateSession A: %v", err)
	}
	if repA.TotalCalls != 3 {
		t.Errorf("aggregate A: want 3 calls, got %d", repA.TotalCalls)
	}
	if repA.Session.Name != "task-alpha" {
		t.Errorf("aggregate A: name = %q, want %q", repA.Session.Name, "task-alpha")
	}

	repB, err := AggregateSession(jsonl, metaB.ID)
	if err != nil {
		t.Fatalf("AggregateSession B: %v", err)
	}
	if repB.TotalCalls != 2 {
		t.Errorf("aggregate B: want 2 calls, got %d", repB.TotalCalls)
	}
}

// TestSession_HookMeta verifies that WriteHookMeta + ReadHookMeta round-trip.
func TestSession_HookMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	meta := HookMeta{
		SessionID:    "ses_20260511_abcd1234",
		InputTokens:  1200,
		OutputTokens: 450,
	}
	if err := WriteHookMeta(dir, meta); err != nil {
		t.Fatalf("WriteHookMeta: %v", err)
	}
	got, ok := ReadHookMeta(dir, meta.SessionID)
	if !ok {
		t.Fatal("ReadHookMeta returned false")
	}
	if got.InputTokens != 1200 || got.OutputTokens != 450 {
		t.Errorf("got %+v, want in=1200 out=450", got)
	}
}

// TestParseHookStdin verifies that the hook stdin parser extracts a
// name hint and token counts from a JSON payload.
func TestParseHookStdin(t *testing.T) {
	t.Parallel()
	payload := `{"prompt":"implement the telemetry export feature for myco","usage":{"input_tokens":3000,"output_tokens":1500}}`
	r := strings.NewReader(payload)
	name, in, out := ParseHookStdin(r)
	if name == "" {
		t.Error("expected non-empty name hint")
	}
	if in != 3000 {
		t.Errorf("input_tokens: got %d, want 3000", in)
	}
	if out != 1500 {
		t.Errorf("output_tokens: got %d, want 1500", out)
	}
}

// TestClassifyTool verifies the exploratory/action classification logic
// that drives the "fallback_exploratory" metric.
func TestClassifyTool(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool     string
		command  string
		wantCat  string
		wantDetail string
	}{
		{"Bash", "grep -r 'foo' src/", "exploratory", "grep"},
		{"Bash", "rg 'pattern' .", "exploratory", "rg"},
		{"Bash", "find . -name '*.go'", "exploratory", "find"},
		{"Bash", "cat internal/telemetry/telemetry.go", "exploratory", "cat"},
		{"Bash", "go test ./...", "action", "go"},
		{"Bash", "/usr/bin/grep foo bar", "exploratory", "grep"},
		{"Bash", "FOO=bar grep thing", "exploratory", "grep"},
		{"Read", "", "exploratory", ""},
		{"Edit", "", "action", ""},
		{"Write", "", "action", ""},
		{"WebSearch", "", "exploratory", ""},
		{"Agent", "", "other", ""},
		{"mcp__mycelium__find_symbol", "", "other", ""},
	}
	for _, tc := range cases {
		cat, detail := ClassifyTool(tc.tool, tc.command)
		if cat != tc.wantCat {
			t.Errorf("ClassifyTool(%q, %q): category = %q, want %q", tc.tool, tc.command, cat, tc.wantCat)
		}
		if detail != tc.wantDetail {
			t.Errorf("ClassifyTool(%q, %q): detail = %q, want %q", tc.tool, tc.command, detail, tc.wantDetail)
		}
	}
}

// TestParsePostToolUse verifies that myco MCP calls are skipped and that
// Bash calls are correctly parsed and classified.
func TestParsePostToolUse(t *testing.T) {
	t.Parallel()

	bashPayload := `{"tool_name":"Bash","tool_input":{"command":"grep -r StartSession internal/"}}`
	rec, ok := ParsePostToolUse(strings.NewReader(bashPayload), "ses_test")
	if !ok {
		t.Fatal("expected ok=true for Bash payload")
	}
	if rec.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", rec.ToolName)
	}
	if rec.Category != "exploratory" {
		t.Errorf("Category = %q, want exploratory", rec.Category)
	}
	if rec.Detail != "grep" {
		t.Errorf("Detail = %q, want grep", rec.Detail)
	}
	if rec.SessionID != "ses_test" {
		t.Errorf("SessionID = %q, want ses_test", rec.SessionID)
	}

	// MCP calls must be skipped.
	mcpPayload := `{"tool_name":"mcp__mycelium__find_symbol","tool_input":{"name":"foo"}}`
	_, ok = ParsePostToolUse(strings.NewReader(mcpPayload), "ses_test")
	if ok {
		t.Error("expected ok=false for mcp__ tool")
	}

	// Read tool.
	readPayload := `{"tool_name":"Read","tool_input":{"file_path":"main.go"}}`
	rec, ok = ParsePostToolUse(strings.NewReader(readPayload), "ses_test")
	if !ok {
		t.Fatal("expected ok=true for Read payload")
	}
	if rec.Category != "exploratory" {
		t.Errorf("Read: Category = %q, want exploratory", rec.Category)
	}
}

// TestExternalRoundTrip verifies AppendExternal + SummarizeExternal.
func TestExternalRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sid := "ses_roundtrip"
	path := ExternalPath(dir, sid)

	records := []ExternalRecord{
		{ToolName: "Bash", Category: "exploratory", Detail: "grep", SessionID: sid},
		{ToolName: "Bash", Category: "exploratory", Detail: "grep", SessionID: sid},
		{ToolName: "Read", Category: "exploratory", SessionID: sid},
		{ToolName: "Edit", Category: "action", SessionID: sid},
	}
	for _, r := range records {
		if err := AppendExternal(path, r); err != nil {
			t.Fatalf("AppendExternal: %v", err)
		}
	}

	summaries, err := SummarizeExternal(path)
	if err != nil {
		t.Fatalf("SummarizeExternal: %v", err)
	}

	byTool := map[string]ExternalSummary{}
	for _, s := range summaries {
		byTool[s.Tool] = s
	}
	if byTool["Bash/grep"].Count != 2 {
		t.Errorf("Bash/grep count = %d, want 2", byTool["Bash/grep"].Count)
	}
	if byTool["Read"].Count != 1 {
		t.Errorf("Read count = %d, want 1", byTool["Read"].Count)
	}

	exploratory := TotalExploratory(summaries)
	if exploratory != 3 {
		t.Errorf("TotalExploratory = %d, want 3", exploratory)
	}
}

// TestSession_SessionIDPropagation checks that the FileRecorder stamps
// the correct session ID onto records when the session sidecar changes.
func TestSession_SessionIDPropagation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "telemetry.jsonl")
	sessionFile := filepath.Join(dir, "current_session.json")

	rec, err := Open(jsonl)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec.SetSessionFile(sessionFile)

	// Record before any session — should have empty session ID.
	_ = rec.Record(Record{Tool: "stats", OK: true})

	// Start session.
	meta, err := StartSession(jsonl, sessionFile, "tagged")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Record after session start — should carry the session ID.
	_ = rec.Record(Record{Tool: "find_symbol", OK: true})
	rec.Close()

	// Read the raw JSONL and verify.
	b, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	// lines[0] = stats (no sid), lines[1] = session_start marker, lines[2] = find_symbol (with sid)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	if strings.Contains(lines[0], `"sid"`) {
		t.Error("first record should have no session ID")
	}
	if !strings.Contains(lines[2], meta.ID) {
		t.Errorf("third record should contain session ID %q; got: %s", meta.ID, lines[2])
	}
}
