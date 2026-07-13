package telemetry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionMeta is the lightweight descriptor written to
// .mycelium/current_session.json and embedded in every
// session_start JSONL marker. Kept small: the JSONL is the source of
// truth for call data; this file just tells the daemon which session is
// active right now.
//
// ClaudeSessionID and TranscriptPath are set when the session was started
// via a Claude Code UserPromptSubmit hook — they link the myco session to
// the agent's conversation so the export can include transcript metrics.
type SessionMeta struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	StartedAt       time.Time `json:"started_at"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	TranscriptPath  string    `json:"transcript_path,omitempty"`
}

// SessionReport is the result of AggregateSession: per-tool stats plus
// the session's own metadata and the wall-clock window of calls.
type SessionReport struct {
	Session      SessionMeta
	Summaries    []Summary
	CallDuration time.Duration // time from first call to last call (not total wall time)
	InputBytes   int64
	OutputBytes  int64
	TotalCalls   int
}

// HookMeta is the optional annotation written by `myco session annotate`
// (called from a Claude Code Stop hook). All fields are best-effort:
// the command succeeds even if the hook can't provide them.
type HookMeta struct {
	SessionID    string    `json:"sid"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	RecordedAt   time.Time `json:"recorded_at"`
}

// StartSession writes a session_start marker to the JSONL log and
// atomically updates the session sidecar file so the running daemon
// picks up the new session ID on its next Record() call.
//
// name may be empty; a timestamp slug is used in that case.
// sessionFilePath is the path of the .mycelium/current_session.json
// sidecar; jsonlPath is the telemetry log.
// claudeSessionID and transcriptPath are optional; pass empty strings when
// starting a session manually rather than via a Claude Code hook.
func StartSession(jsonlPath, sessionFilePath, name, claudeSessionID, transcriptPath string) (SessionMeta, error) {
	if name == "" {
		name = time.Now().Format("2006-01-02 15:04")
	}
	// When Claude Code's hook handed us a session UUID, use it as the myco
	// session ID directly so the user can copy-paste the same string across
	// both systems. Manual `myco session start` (no hook) still gets a
	// `ses_<date>_<rand>` ID.
	id := claudeSessionID
	if id == "" {
		id = newSessionID()
	}
	meta := SessionMeta{
		ID:              id,
		Name:            name,
		StartedAt:       time.Now(),
		ClaudeSessionID: claudeSessionID,
		TranscriptPath:  transcriptPath,
	}

	// Write the JSONL marker first so the log is consistent even if the
	// sidecar write fails.
	if err := appendSessionMarker(jsonlPath, meta); err != nil {
		return SessionMeta{}, fmt.Errorf("write session marker: %w", err)
	}

	// Atomically replace the sidecar via a tmp-file rename so the daemon
	// never reads a half-written file.
	if err := writeSessionFile(sessionFilePath, meta); err != nil {
		return SessionMeta{}, fmt.Errorf("write session file: %w", err)
	}
	return meta, nil
}

// LoadCurrentSession reads the session sidecar. Returns (zero, false) if
// the file is absent or unreadable.
func LoadCurrentSession(sessionFilePath string) (SessionMeta, bool) {
	b, err := os.ReadFile(sessionFilePath)
	if err != nil {
		return SessionMeta{}, false
	}
	var m SessionMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return SessionMeta{}, false
	}
	return m, true
}

// ListSessions scans the JSONL log and returns one SessionReport per
// session_start marker, with call counts aggregated from subsequent
// records. Returns (nil, nil) when the file doesn't exist.
func ListSessions(jsonlPath string) ([]SessionReport, error) {
	records, err := scanAll(jsonlPath)
	if err != nil {
		return nil, err
	}
	return buildReports(records), nil
}

// AggregateSession filters the JSONL to a single session and returns its
// report. Returns an error if the session ID is not found.
func AggregateSession(jsonlPath, sessionID string) (SessionReport, error) {
	records, err := scanAll(jsonlPath)
	if err != nil {
		return SessionReport{}, err
	}
	reports := buildReports(records)
	for _, r := range reports {
		if r.Session.ID == sessionID {
			return r, nil
		}
	}
	return SessionReport{}, fmt.Errorf("session %q not found", sessionID)
}

// WriteHookMeta writes Claude Code hook annotations (token counts, etc.)
// to a per-session sidecar at <dir>/session_<id>_meta.json.
func WriteHookMeta(dir string, meta HookMeta) error {
	if meta.RecordedAt.IsZero() {
		meta.RecordedAt = time.Now()
	}
	path := filepath.Join(dir, "session_"+meta.SessionID+"_meta.json")
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ReadHookMeta reads the per-session hook annotation sidecar. Returns
// (zero, false) when none exists — token data is always optional.
func ReadHookMeta(dir, sessionID string) (HookMeta, bool) {
	path := filepath.Join(dir, "session_"+sessionID+"_meta.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return HookMeta{}, false
	}
	var m HookMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return HookMeta{}, false
	}
	return m, true
}

// HookStdinData is the parsed result of a Claude Code hook's stdin payload.
// All fields are best-effort — hook versions and contexts vary.
type HookStdinData struct {
	Name            string // first 8 words of the prompt, for session naming
	InputTokens     int
	OutputTokens    int
	ClaudeSessionID string // Claude Code's UUID for this conversation
	TranscriptPath  string // absolute path to the conversation JSONL
	StopHookActive  bool   // Stop hook: true when Claude is already continuing because a Stop hook blocked
}

// ParseHookStdin extracts all useful fields from the JSON that Claude Code
// pipes to hooks via stdin. Fields it doesn't recognise are silently ignored.
func ParseHookStdin(r io.Reader) HookStdinData {
	var payload struct {
		// Common fields across hook types
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		// UserPromptSubmit fields
		Prompt string `json:"prompt"`
		// Some Claude Code versions nest the message
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		// Stop hook fields — Anthropic SDK usage block
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		// Also try top-level token fields
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		// Stop hook: set when a previous block decision already forced
		// Claude to continue — blocking again risks a loop.
		StopHookActive bool `json:"stop_hook_active"`
	}
	b, err := io.ReadAll(r)
	if err != nil || len(b) == 0 {
		return HookStdinData{}
	}
	_ = json.Unmarshal(b, &payload)

	var d HookStdinData
	d.ClaudeSessionID = payload.SessionID
	d.TranscriptPath = payload.TranscriptPath
	d.StopHookActive = payload.StopHookActive

	// Build a name hint from the first ~8 words of the prompt text.
	text := payload.Prompt
	if text == "" {
		text = payload.Message.Content
	}
	if text != "" {
		text = strings.TrimSpace(text)
		words := strings.Fields(text)
		if len(words) > 8 {
			words = words[:8]
		}
		d.Name = strings.Join(words, " ")
	}

	d.InputTokens = payload.Usage.InputTokens
	if d.InputTokens == 0 {
		d.InputTokens = payload.InputTokens
	}
	d.OutputTokens = payload.Usage.OutputTokens
	if d.OutputTokens == 0 {
		d.OutputTokens = payload.OutputTokens
	}
	return d
}

// ─── internals ───────────────────────────────────────────────────────────────

func newSessionID() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return "ses_" + time.Now().Format("20060102") + "_" + string(b)
}

func appendSessionMarker(jsonlPath string, meta SessionMeta) error {
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	r := Record{
		Timestamp:       meta.StartedAt,
		Tool:            meta.Name,
		Kind:            "session_start",
		SessionID:       meta.ID,
		OK:              true,
		ClaudeSessionID: meta.ClaudeSessionID,
		TranscriptPath:  meta.TranscriptPath,
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func writeSessionFile(path string, meta SessionMeta) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type rawRecord struct {
	Record
	// Alias so we can detect session markers without a second unmarshal.
	kind string
}

func scanAll(jsonlPath string) ([]Record, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open telemetry log: %w", err)
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read telemetry log: %w", err)
	}
	return out, nil
}

// buildReports groups records by session. Records before the first
// session marker get assigned to a synthetic "<untagged>" session only
// when they carry a non-empty SessionID themselves (legacy records or
// records from a session whose start marker is in a rotated-away log
// are silently skipped in the listing — they still appear in
// AggregateSession if called with the right ID).
func buildReports(records []Record) []SessionReport {
	type bucket struct {
		meta  SessionMeta
		calls []Record
	}

	var (
		buckets []*bucket
		byID    = map[string]*bucket{}
		current *bucket
	)

	for _, r := range records {
		if r.Kind == "session_start" {
			b := &bucket{
				meta: SessionMeta{
					ID:              r.SessionID,
					Name:            r.Tool,
					StartedAt:       r.Timestamp,
					ClaudeSessionID: r.ClaudeSessionID,
					TranscriptPath:  r.TranscriptPath,
				},
			}
			buckets = append(buckets, b)
			byID[r.SessionID] = b
			current = b
			continue
		}
		if r.SessionID != "" {
			if b, ok := byID[r.SessionID]; ok {
				b.calls = append(b.calls, r)
				continue
			}
			// Session marker not seen yet (e.g. rotated log) — create a
			// synthetic bucket so calls are still attributable.
			b := &bucket{
				meta: SessionMeta{ID: r.SessionID, Name: "<unknown>"},
			}
			buckets = append(buckets, b)
			byID[r.SessionID] = b
			b.calls = append(b.calls, r)
			continue
		}
		// No session ID: attribute to the most recent session_start marker.
		if current != nil {
			current.calls = append(current.calls, r)
		}
	}

	out := make([]SessionReport, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, rollup(b.meta, b.calls))
	}
	// Most recent first.
	sort.Slice(out, func(i, j int) bool {
		return out[i].Session.StartedAt.After(out[j].Session.StartedAt)
	})
	return out
}

func rollup(meta SessionMeta, calls []Record) SessionReport {
	rep := SessionReport{Session: meta}
	if len(calls) == 0 {
		return rep
	}
	// Build per-tool summaries using the same aggregator as Aggregate().
	byTool := map[string]*accum{}
	all := &accum{}
	for _, r := range calls {
		dur := time.Duration(r.DurationMS) * time.Millisecond
		a, ok := byTool[r.Tool]
		if !ok {
			a = &accum{}
			byTool[r.Tool] = a
		}
		for _, target := range []*accum{a, all} {
			target.count++
			if r.OK {
				target.ok++
			}
			target.inBytes += int64(r.InputBytes)
			target.outBytes += int64(r.OutputBytes)
			target.durations = append(target.durations, dur)
			if target.first.IsZero() || r.Timestamp.Before(target.first) {
				target.first = r.Timestamp
			}
			if r.Timestamp.After(target.last) {
				target.last = r.Timestamp
			}
		}
	}
	tools := make([]Summary, 0, len(byTool))
	for tool, a := range byTool {
		tools = append(tools, summarize(tool, a))
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Count > tools[j].Count })
	rep.Summaries = tools
	rep.TotalCalls = all.count
	rep.InputBytes = all.inBytes
	rep.OutputBytes = all.outBytes
	if !all.first.IsZero() && !all.last.IsZero() {
		rep.CallDuration = all.last.Sub(all.first)
	}
	return rep
}
