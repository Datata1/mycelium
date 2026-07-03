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
	metaA, err := StartSession(jsonl, sessionFile, "task-alpha", "", "")
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
	metaB, err := StartSession(jsonl, sessionFile, "task-beta", "", "")
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
	payload := `{"session_id":"abc-123","transcript_path":"/tmp/abc.jsonl","prompt":"implement the telemetry export feature for myco","usage":{"input_tokens":3000,"output_tokens":1500}}`
	r := strings.NewReader(payload)
	d := ParseHookStdin(r)
	if d.Name == "" {
		t.Error("expected non-empty name hint")
	}
	if d.InputTokens != 3000 {
		t.Errorf("input_tokens: got %d, want 3000", d.InputTokens)
	}
	if d.OutputTokens != 1500 {
		t.Errorf("output_tokens: got %d, want 1500", d.OutputTokens)
	}
	if d.ClaudeSessionID != "abc-123" {
		t.Errorf("claude_session_id: got %q, want abc-123", d.ClaudeSessionID)
	}
	if d.TranscriptPath != "/tmp/abc.jsonl" {
		t.Errorf("transcript_path: got %q, want /tmp/abc.jsonl", d.TranscriptPath)
	}
}

// TestClassifyTool verifies the exploratory/action classification logic
// that drives the "fallback_exploratory" metric.
func TestClassifyTool(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool       string
		command    string
		wantCat    string
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

// TestParsePostToolUse_CapturesOutputSize is the v3.4 A1 contract: when
// Claude Code pipes a `tool_response` field alongside `tool_name` and
// `tool_input`, ParsePostToolUse records its byte length so A2 can
// build a session-cost estimate.
func TestParsePostToolUse_CapturesOutputSize(t *testing.T) {
	t.Parallel()

	// Read tool result: a file's contents inlined in the JSON.
	readPayload := `{
		"tool_name":"Read",
		"tool_input":{"file_path":"main.go"},
		"tool_response":{"file":{"contents":"package main\nfunc main(){}\n"}}
	}`
	rec, ok := ParsePostToolUse(strings.NewReader(readPayload), "ses_test")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if rec.OutputSize == 0 {
		t.Fatal("OutputSize is 0; A1 contract requires non-zero when tool_response present")
	}
	// Sanity: the response payload is the inner JSON object; should be
	// at least the length of the literal "package main\nfunc main(){}\n".
	if rec.OutputSize < 30 {
		t.Errorf("OutputSize = %d, expected ≥ 30 (file contents present)", rec.OutputSize)
	}

	// Bash with stdout/stderr.
	bashPayload := `{
		"tool_name":"Bash",
		"tool_input":{"command":"grep -r foo ."},
		"tool_response":{"stdout":"line1\nline2\nline3\n","stderr":"","interrupted":false}
	}`
	rec, ok = ParsePostToolUse(strings.NewReader(bashPayload), "ses_test")
	if !ok {
		t.Fatal("expected ok=true for Bash")
	}
	if rec.OutputSize == 0 {
		t.Error("Bash: OutputSize is 0")
	}

	// Legacy payload without tool_response (e.g. older Claude Code, or
	// tools we haven't accounted for) → OutputSize 0, still recorded.
	legacyPayload := `{"tool_name":"Bash","tool_input":{"command":"ls"}}`
	rec, ok = ParsePostToolUse(strings.NewReader(legacyPayload), "ses_test")
	if !ok {
		t.Fatal("expected ok=true on legacy payload")
	}
	if rec.OutputSize != 0 {
		t.Errorf("legacy payload: OutputSize = %d, want 0", rec.OutputSize)
	}
	if rec.InputSize == 0 {
		t.Error("legacy payload: InputSize is 0, should still record input")
	}
}

// TestSummarizeExternal_AggregatesBytes is the v3.4 A1 aggregator
// contract: per-tool summaries sum input_size and output_size across
// all records so callers can produce a "this tool category cost N
// bytes" line without re-streaming the JSONL.
func TestSummarizeExternal_AggregatesBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sid := "ses_bytes"
	path := ExternalPath(dir, sid)

	records := []ExternalRecord{
		{ToolName: "Read", Category: "exploratory", SessionID: sid, InputSize: 50, OutputSize: 1000},
		{ToolName: "Read", Category: "exploratory", SessionID: sid, InputSize: 50, OutputSize: 2000},
		{ToolName: "Bash", Category: "exploratory", Detail: "grep", SessionID: sid, InputSize: 30, OutputSize: 500},
		{ToolName: "Edit", Category: "action", SessionID: sid, InputSize: 200, OutputSize: 10},
	}
	for _, r := range records {
		if err := AppendExternal(path, r); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	summaries, err := SummarizeExternal(path)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	byTool := map[string]ExternalSummary{}
	for _, s := range summaries {
		byTool[s.Tool] = s
	}
	if got := byTool["Read"].OutputBytes; got != 3000 {
		t.Errorf("Read OutputBytes = %d, want 3000", got)
	}
	if got := byTool["Read"].InputBytes; got != 100 {
		t.Errorf("Read InputBytes = %d, want 100", got)
	}
	if got := byTool["Bash/grep"].OutputBytes; got != 500 {
		t.Errorf("Bash/grep OutputBytes = %d, want 500", got)
	}

	inBytes, outBytes := TotalExternalBytes(summaries)
	if inBytes != 50+50+30+200 {
		t.Errorf("TotalExternalBytes input = %d, want %d", inBytes, 50+50+30+200)
	}
	if outBytes != 1000+2000+500+10 {
		t.Errorf("TotalExternalBytes output = %d, want %d", outBytes, 1000+2000+500+10)
	}
}

// TestComputeSessionCost_RollsBytesAndTokens is the v3.4 A2 contract:
// myco summaries + fallback summaries fold into a single cost block
// with byte totals split by source plus a token estimate via the
// configurable chars-per-token ratio. The "all" rollup row in the myco
// summaries is excluded so it doesn't double-count.
func TestComputeSessionCost_RollsBytesAndTokens(t *testing.T) {
	t.Parallel()
	myco := []Summary{
		{Tool: "find_symbol", Count: 5, InputBytes: 100, OutputBytes: 5_000},
		{Tool: "read_focused", Count: 3, InputBytes: 60, OutputBytes: 30_000},
		// Synthetic "all" rollup the aggregator appends; ComputeSessionCost
		// must skip this row.
		{Tool: "all", Count: 8, InputBytes: 160, OutputBytes: 35_000},
	}
	fallback := []ExternalSummary{
		{Tool: "Read", Category: "exploratory", Count: 4, InputBytes: 40, OutputBytes: 20_000},
		{Tool: "Bash/grep", Category: "exploratory", Count: 1, InputBytes: 10, OutputBytes: 500},
	}

	got := ComputeSessionCost(myco, fallback, 4.0)

	if got.CharsPerToken != 4.0 {
		t.Errorf("CharsPerToken = %v, want 4.0", got.CharsPerToken)
	}
	if got.MycoOutputBytes != 35_000 {
		t.Errorf("MycoOutputBytes = %d, want 35000", got.MycoOutputBytes)
	}
	if got.MycoInputBytes != 160 {
		t.Errorf("MycoInputBytes = %d, want 160 (must exclude 'all' row)", got.MycoInputBytes)
	}
	if got.FallbackOutputBytes != 20_500 {
		t.Errorf("FallbackOutputBytes = %d, want 20500", got.FallbackOutputBytes)
	}
	if got.TotalBytes != 160+35_000+50+20_500 {
		t.Errorf("TotalBytes = %d, want %d", got.TotalBytes, 160+35_000+50+20_500)
	}
	wantTokens := int64((float64(got.TotalBytes) / 4.0) + 0.5)
	if got.EstimatedTokens != wantTokens {
		t.Errorf("EstimatedTokens = %d, want %d", got.EstimatedTokens, wantTokens)
	}
	if got.MycoTokens+got.FallbackTokens != got.EstimatedTokens {
		t.Errorf("token split %d+%d != total %d",
			got.MycoTokens, got.FallbackTokens, got.EstimatedTokens)
	}

	// Per-tool rows: one per non-"all" source. Ranked by TotalBytes desc.
	if n := len(got.PerTool); n != 4 {
		t.Fatalf("PerTool len = %d, want 4", n)
	}
	if got.PerTool[0].Tool != "read_focused" {
		t.Errorf("top tool = %q, want read_focused (largest TotalBytes)",
			got.PerTool[0].Tool)
	}
	if got.PerTool[0].Source != "myco" {
		t.Errorf("top tool source = %q, want myco", got.PerTool[0].Source)
	}
	for _, p := range got.PerTool {
		if p.Tool == "all" {
			t.Error("PerTool should not contain the synthetic 'all' row")
		}
		if p.TotalBytes != p.InputBytes+p.OutputBytes {
			t.Errorf("%s: TotalBytes %d != %d+%d",
				p.Tool, p.TotalBytes, p.InputBytes, p.OutputBytes)
		}
	}
}

// TestComputeSessionCost_DefaultRatio: a non-positive charsPerToken
// falls back to 4.0 silently so callers can pass cfg.Telemetry.CharsPerToken
// unconditionally without a nil-check.
func TestComputeSessionCost_DefaultRatio(t *testing.T) {
	t.Parallel()
	myco := []Summary{{Tool: "find_symbol", Count: 1, OutputBytes: 4000}}
	cost := ComputeSessionCost(myco, nil, 0)
	if cost.CharsPerToken != 4.0 {
		t.Errorf("CharsPerToken = %v, want 4.0 fallback", cost.CharsPerToken)
	}
	if cost.EstimatedTokens != 1000 {
		t.Errorf("EstimatedTokens = %d, want 1000 (4000 bytes / 4)",
			cost.EstimatedTokens)
	}

	costNeg := ComputeSessionCost(myco, nil, -2.5)
	if costNeg.CharsPerToken != 4.0 {
		t.Errorf("negative ratio: CharsPerToken = %v, want 4.0", costNeg.CharsPerToken)
	}
}

// TestEstimateCounterfactual_KnownAndUnknown is the v3.4 A3 contract for
// the per-tool model: known tools return (multiplier × outputBytes,
// quality), unknown tools return {0, none}, and zero-multiplier tools
// (stats / ping) return {0, none}.
func TestEstimateCounterfactual_KnownAndUnknown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool        string
		out         int64
		wantBytes   int64
		wantQuality EstimateQuality
	}{
		{"find_symbol", 5_000, 4_000, EstimateQualityMedium},  // 5000 × 0.8
		{"read_focused", 10_000, 40_000, EstimateQualityHigh}, // 10000 × 4.0 (post-B1 preview-mode lighter than Read)
		{"search_lexical", 3_000, 3_000, EstimateQualityHigh}, // 3000 × 1.0
		{"get_neighborhood", 1_000, 2_500, EstimateQualityLow},
		{"stats", 500, 0, EstimateQualityNone},          // zero multiplier → no estimate
		{"unknown_tool", 1_000, 0, EstimateQualityNone}, // missing entry → no estimate
	}
	for _, tc := range cases {
		got := EstimateCounterfactual(tc.tool, tc.out)
		if got.Bytes != tc.wantBytes {
			t.Errorf("%s: bytes = %d, want %d", tc.tool, got.Bytes, tc.wantBytes)
		}
		if got.Quality != tc.wantQuality {
			t.Errorf("%s: quality = %q, want %q", tc.tool, got.Quality, tc.wantQuality)
		}
	}
}

// TestComputeSessionCost_Counterfactual is the v3.4 A3 aggregator
// contract: per-row counterfactual estimates roll up into
// MycoCounterfactualBytes / WithoutMycoEstimateBytes / EstimatedSavingsBytes
// / SavingsRatio, and the quality-mix map counts each call by its
// estimate quality so renderers can surface the trust level.
func TestComputeSessionCost_Counterfactual(t *testing.T) {
	t.Parallel()
	myco := []Summary{
		{Tool: "find_symbol", Count: 5, OK: 5, OutputBytes: 5_000, OutputBytesOK: 5_000},    // 0.8 medium → 4000
		{Tool: "read_focused", Count: 3, OK: 3, OutputBytes: 30_000, OutputBytesOK: 30_000}, // 4.0 high → 120000 (v4 B1 preview mode)
		{Tool: "stats", Count: 2, OK: 2, OutputBytes: 200, OutputBytesOK: 200},              // 0 none → skipped
	}
	fallback := []ExternalSummary{
		{Tool: "Read", Count: 4, InputBytes: 40, OutputBytes: 20_000},
	}

	cost := ComputeSessionCost(myco, fallback, 4.0)

	// Per-row counterfactual on PerTool entries.
	byTool := map[string]ToolCost{}
	for _, p := range cost.PerTool {
		byTool[p.Tool] = p
	}
	if got := byTool["find_symbol"].CounterfactualBytes; got != 4_000 {
		t.Errorf("find_symbol cf = %d, want 4000", got)
	}
	if got := byTool["find_symbol"].EstimateQuality; got != EstimateQualityMedium {
		t.Errorf("find_symbol quality = %q, want medium", got)
	}
	if got := byTool["read_focused"].CounterfactualBytes; got != 120_000 {
		t.Errorf("read_focused cf = %d, want 120000", got)
	}
	if got := byTool["stats"].CounterfactualBytes; got != 0 {
		t.Errorf("stats cf = %d, want 0 (no fallback)", got)
	}
	// Fallback rows should never carry a counterfactual — they ARE the
	// fallback.
	if got := byTool["Read"].CounterfactualBytes; got != 0 {
		t.Errorf("Read (fallback) cf = %d, want 0", got)
	}

	// Aggregate counterfactual sums. find_symbol → 4000, read_focused → 120000.
	if cost.MycoCounterfactualBytes != 124_000 {
		t.Errorf("MycoCounterfactualBytes = %d, want 124000", cost.MycoCounterfactualBytes)
	}
	wantWithout := int64(124_000 + 40 + 20_000)
	if cost.WithoutMycoEstimateBytes != wantWithout {
		t.Errorf("WithoutMycoEstimateBytes = %d, want %d",
			cost.WithoutMycoEstimateBytes, wantWithout)
	}
	wantSavings := wantWithout - cost.TotalBytes
	if cost.EstimatedSavingsBytes != wantSavings {
		t.Errorf("EstimatedSavingsBytes = %d, want %d",
			cost.EstimatedSavingsBytes, wantSavings)
	}
	// Post-v4-B1: read_focused multiplier of 4.0× means the no-focus
	// preview is genuinely lighter than the modelled fallback (full
	// Read), so the aggregate savings goes positive. This is the
	// inverse of the pre-B1 fixture state — the model now correctly
	// rewards myco when it returns less than the equivalent Read.
	if cost.SavingsRatio <= 0 {
		t.Errorf("SavingsRatio = %v, want > 0 (post-B1 read_focused saves bytes vs Read)",
			cost.SavingsRatio)
	}
	if cost.MycoCounterfactualTokens == 0 {
		t.Error("MycoCounterfactualTokens should be > 0 once cf bytes are non-zero")
	}

	// Quality mix counts calls per quality bucket. find_symbol contributes
	// 5 medium, read_focused contributes 3 high. stats has none, so no entry.
	if got := cost.CounterfactualQualityMix[EstimateQualityMedium]; got != 5 {
		t.Errorf("quality mix medium = %d, want 5", got)
	}
	if got := cost.CounterfactualQualityMix[EstimateQualityHigh]; got != 3 {
		t.Errorf("quality mix high = %d, want 3", got)
	}
	if _, ok := cost.CounterfactualQualityMix[EstimateQualityNone]; ok {
		t.Error("quality mix should not include 'none' bucket")
	}
}

// TestComputeSessionCost_NegativeSavings codifies the honest-output case:
// when every myco call is `stats` (no fallback exists) and there are no
// real fallback calls, the modelled "without-myco" cost is 0 while the
// actual myco cost is positive — savings goes negative. Renderers must
// not silently clamp it; the negative number is the signal that myco
// added overhead without saving anything (e.g. adoption-fixed-point gap).
func TestComputeSessionCost_NegativeSavings(t *testing.T) {
	t.Parallel()
	myco := []Summary{
		{Tool: "stats", Count: 3, InputBytes: 30, OutputBytes: 1_500},
	}
	cost := ComputeSessionCost(myco, nil, 4.0)
	if cost.MycoCounterfactualBytes != 0 {
		t.Errorf("MycoCounterfactualBytes = %d, want 0 (stats has no fallback)",
			cost.MycoCounterfactualBytes)
	}
	if cost.EstimatedSavingsBytes >= 0 {
		t.Errorf("EstimatedSavingsBytes = %d, want < 0 (myco-only cost should look like a loss vs zero counterfactual)",
			cost.EstimatedSavingsBytes)
	}
	if cost.SavingsRatio != 0 {
		// WithoutMycoEstimateBytes is 0, so SavingsRatio stays 0 by the
		// guard in ComputeSessionCost. The renderer treats 0 as "no
		// comparison possible"; the EstimatedSavingsBytes carries the
		// honest negative number.
		t.Errorf("SavingsRatio = %v, want 0 when WithoutMycoEstimateBytes is 0",
			cost.SavingsRatio)
	}
}

// TestComputeSessionCost_EmptyInputs: zero summaries → zero costs, no
// panics, no division-by-zero in the token conversion.
func TestComputeSessionCost_EmptyInputs(t *testing.T) {
	t.Parallel()
	cost := ComputeSessionCost(nil, nil, 4.0)
	if cost.TotalBytes != 0 || cost.EstimatedTokens != 0 {
		t.Errorf("empty inputs: TotalBytes=%d EstimatedTokens=%d, want 0/0",
			cost.TotalBytes, cost.EstimatedTokens)
	}
	if len(cost.PerTool) != 0 {
		t.Errorf("empty inputs: PerTool len = %d, want 0", len(cost.PerTool))
	}
}

// TestClaudeProjectSlug pins the directory-name convention Claude Code uses
// under ~/.claude/projects/. A regression here previously caused every
// `myco session transcript` auto-discovery to fail: an earlier version
// stripped the leading "-", so /Users/x/repo was looked up as
// "Users-x-repo" instead of the real "-Users-x-repo".
func TestClaudeProjectSlug(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/Users/codesphere/monorepo-4": "-Users-codesphere-monorepo-4",
		"/home/jd/work/myco":           "-home-jd-work-myco",
		"/tmp":                         "-tmp",
	}
	for in, want := range cases {
		if got := ClaudeProjectSlug(in); got != want {
			t.Errorf("ClaudeProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSession_ClaudeIDAsSessionID checks that when StartSession is given a
// Claude session UUID it adopts that UUID as the primary session ID — the
// UX fix that lets users copy-paste the same ID across myco and Claude Code.
// When no UUID is provided, the generated `ses_<date>_<rand>` ID is used.
func TestSession_ClaudeIDAsSessionID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "telemetry.jsonl")
	sf := filepath.Join(dir, "current_session.json")

	uuid := "5641b7bd-7435-4a86-8550-945511108cda"
	tpath := "/tmp/transcript.jsonl"
	meta, err := StartSession(jsonl, sf, "hooked", uuid, tpath)
	if err != nil {
		t.Fatalf("StartSession with UUID: %v", err)
	}
	if meta.ID != uuid {
		t.Errorf("ID = %q, want Claude UUID %q", meta.ID, uuid)
	}
	if meta.TranscriptPath != tpath {
		t.Errorf("TranscriptPath = %q, want %q", meta.TranscriptPath, tpath)
	}

	// And: the JSONL marker carries both fields so `myco session transcript`
	// can resolve them after the active-session sidecar has been overwritten.
	rep, err := AggregateSession(jsonl, uuid)
	if err != nil {
		t.Fatalf("AggregateSession: %v", err)
	}
	if rep.Session.ClaudeSessionID != uuid {
		t.Errorf("aggregated ClaudeSessionID = %q, want %q", rep.Session.ClaudeSessionID, uuid)
	}
	if rep.Session.TranscriptPath != tpath {
		t.Errorf("aggregated TranscriptPath = %q, want %q", rep.Session.TranscriptPath, tpath)
	}

	// No-UUID path keeps the generated ses_ ID.
	meta2, err := StartSession(jsonl, sf, "manual", "", "")
	if err != nil {
		t.Fatalf("StartSession no UUID: %v", err)
	}
	if !strings.HasPrefix(meta2.ID, "ses_") {
		t.Errorf("manual session ID = %q, want ses_ prefix", meta2.ID)
	}
}

// TestIsWrapperOnly_v4_T7 pins the v4 T7 fix: ParseTranscript must
// skip user messages whose body is exclusively an IDE wrapper tag
// (`<ide_opened_file>...</ide_opened_file>`, `<system-reminder>...`,
// etc.) so the session export's `Task` field shows the user's real
// prose, not auto-injected context. F1/T7 surfaced this against a
// monorepo-4 export where `Task: <ide_opened_file>...</ide_opened_file>`
// replaced the actual PR-feedback prompt.
func TestIsWrapperOnly_v4_T7(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"only-ide-wrapper", "<ide_opened_file>The user opened the file /x.ts in the IDE.</ide_opened_file>", true},
		{"only-ide-wrapper-with-trailing-ws", "<ide_opened_file>foo</ide_opened_file>\n  ", true},
		{"system-reminder-only", "<system-reminder>token used</system-reminder>", true},
		{"command-name-only", "<command-name>/usage</command-name>", true},
		{"prose-then-wrapper", "Please fix this. <ide_opened_file>foo</ide_opened_file>", false},
		{"wrapper-then-prose", "<ide_opened_file>foo</ide_opened_file>\n\nNow do this.", false},
		{"plain-prose", "I got feedback on my PR regarding this feature branch.", false},
		{"prose-with-tag-name-but-no-wrapping", "We use <ide_opened_file> as a marker", false},
	}
	for _, tc := range cases {
		got := isWrapperOnly(tc.in)
		if got != tc.want {
			t.Errorf("%s: isWrapperOnly(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestParseTranscript_NestedFormat verifies the parser understands the
// current Claude Code JSONL schema where role + content live under a
// nested `message` object. A regression here previously made every
// transcript export return "empty or unreadable" — the parser walked
// real conversations but never matched a role and skipped every line.
func TestParseTranscript_NestedFormat(t *testing.T) {
	t.Parallel()
	jsonl := strings.Join([]string{
		`{"type":"queue-operation","operation":"enqueue"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"find the bug"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"mcp__mycelium__find_symbol","input":{"name":"foo"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"text","text":"matched 3 symbols"}]}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"grep -r foo ."}}]}}`,
	}, "\n")

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Summary
	s, err := ParseTranscript(path)
	if err != nil {
		t.Fatalf("ParseTranscript: %v", err)
	}
	if s.ToolCalls != 2 {
		t.Errorf("ToolCalls = %d, want 2", s.ToolCalls)
	}
	if s.MycoCallsFromTranscript != 1 {
		t.Errorf("MycoCallsFromTranscript = %d, want 1", s.MycoCallsFromTranscript)
	}
	if !strings.Contains(s.FirstUserMessage, "find the bug") {
		t.Errorf("FirstUserMessage = %q, want it to contain 'find the bug'", s.FirstUserMessage)
	}

	// Events
	events, err := ParseTranscriptEvents(path)
	if err != nil {
		t.Fatalf("ParseTranscriptEvents: %v", err)
	}
	var sawText, sawMyco, sawFallback, sawResult bool
	for _, e := range events {
		if e.Text == "find the bug" {
			sawText = true
		}
		if e.IsMCO {
			sawMyco = true
		}
		if e.IsFallback && e.ToolName == "Bash" {
			sawFallback = true
		}
		if strings.Contains(e.ToolResult, "matched 3 symbols") {
			sawResult = true
		}
	}
	if !sawText {
		t.Error("expected to see the first user text in events")
	}
	if !sawMyco {
		t.Error("expected to see the mcp__mycelium__ tool_use as IsMCO")
	}
	if !sawFallback {
		t.Error("expected to see the Bash grep call as IsFallback")
	}
	if !sawResult {
		t.Error("expected to extract text from the tool_result block")
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
	meta, err := StartSession(jsonl, sessionFile, "tagged", "", "")
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
