// Package telemetry is mycelium's local-only call-frequency log.
//
// v2.2 (Pillar K of the v3 roadmap). Purpose: collect data on which
// MCP / IPC tools agents actually call, how often, and at what byte
// cost — so v3's adoption-targeted pillars (focused reads, skills
// tree) can be scoped on observation rather than intuition.
//
// Hard rules:
//
//   - **Off by default.** Turned on only via `telemetry.enabled: true`
//     in `.mycelium.yml`. The Disabled recorder is a no-op.
//   - **Local only.** No network, no aggregation, no phoning home.
//     Records land in `.mycelium/telemetry.jsonl` next to the index
//     so they share the .gitignore the index already has.
//   - **Append-only JSONL** so users can `tail -f` and external tools
//     can stream-parse. One JSON object per line.
//   - **Concurrency-safe.** The daemon dispatches multiple requests
//     in parallel; Record acquires a mutex around the file write.
//     Throughput is bounded by sequential fsync cost — the daemon's
//     slowest read query is orders of magnitude more expensive, so
//     this isn't on the hot path.
package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one row in the JSONL log. Field names are short and stable
// because they're the wire format for `myco stats --telemetry` and any
// future analysis tooling.
//
// Kind is empty for normal tool calls (the common case — omitting it
// keeps the log compact). "session_start" marks a new session boundary.
// SessionID is the ID of the session that was active when the call was
// dispatched; empty means no session was running.
type Record struct {
	Timestamp   time.Time `json:"ts"`
	Tool        string    `json:"tool"`
	InputBytes  int       `json:"in_bytes"`
	OutputBytes int       `json:"out_bytes"`
	DurationMS  int64     `json:"dur_ms"`
	OK          bool      `json:"ok"`
	SessionID   string    `json:"sid,omitempty"`
	Kind        string    `json:"kind,omitempty"`
}

// Recorder is the interface every dispatcher consumes. Two
// implementations exist: the Disabled no-op for the default-off case,
// and FileRecorder for telemetry.enabled: true.
type Recorder interface {
	Record(r Record) error
	Close() error
}

// Disabled is the default. Cheaper than nil-checking at every call site.
type Disabled struct{}

func (Disabled) Record(Record) error { return nil }
func (Disabled) Close() error        { return nil }

// FileRecorder appends one JSON line per call to the configured path.
// Open returns an error if the path can't be created or opened in append
// mode; the daemon falls back to Disabled in that case rather than
// failing startup.
//
// When sessionFile is set (via SetSessionFile), each Record() reads that
// file to inject the current session ID. The read is an OS-cached stat on
// a tiny JSON file — negligible overhead relative to the log write itself.
type FileRecorder struct {
	mu          sync.Mutex
	f           *os.File
	path        string
	sessionFile string
}

// Open creates parent directories if needed and opens path in append
// mode. Permissions follow the existing index file — owner-rw, group/
// other-r. Closing the previous handle is the caller's job.
func Open(path string) (*FileRecorder, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("telemetry mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("telemetry open: %w", err)
	}
	return &FileRecorder{f: f, path: path}, nil
}

// Record writes one JSON object terminated by \n. Concurrency-safe.
// Marshal failure is silently dropped — telemetry is observability,
// not correctness, so a corrupt record shouldn't fail the dispatcher.
// Write errors propagate so callers can decide whether to log them.
func (r *FileRecorder) Record(rec Record) error {
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.SessionID == "" {
		rec.SessionID = r.readCurrentSessionID()
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil
	}
	b = append(b, '\n')
	_, err = r.f.Write(b)
	return err
}

// Close flushes and closes the underlying file. Safe to call multiple
// times; subsequent calls return nil.
func (r *FileRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Path returns the filesystem location the recorder writes to. Used by
// `myco stats --telemetry` to find the log when telemetry is configured
// but the daemon isn't running.
func (r *FileRecorder) Path() string { return r.path }

// SetSessionFile configures the path to the current-session sidecar
// (.mycelium/current_session.json). Once set, every subsequent Record()
// call reads that file and stamps its SessionID onto the record. This
// lets `myco session start` signal the daemon without an IPC round-trip.
func (r *FileRecorder) SetSessionFile(path string) {
	r.mu.Lock()
	r.sessionFile = path
	r.mu.Unlock()
}

// readCurrentSessionID reads the session sidecar and returns the ID,
// or "" if the file is absent or unparseable. Called under r.mu.
func (r *FileRecorder) readCurrentSessionID() string {
	if r.sessionFile == "" {
		return ""
	}
	b, err := os.ReadFile(r.sessionFile)
	if err != nil {
		return ""
	}
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	return m.ID
}
