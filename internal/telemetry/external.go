package telemetry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// ExternalRecord is one row in the per-session external tool log
// (.mycelium/session_<id>_external.jsonl). It captures every non-myco
// tool call so the export can show whether the agent fell back to raw
// filesystem exploration when myco's output wasn't sufficient.
type ExternalRecord struct {
	Timestamp time.Time `json:"ts"`
	SessionID string    `json:"sid"`
	ToolName  string    `json:"tool"`           // "Bash", "Read", "WebSearch", …
	Category  string    `json:"category"`       // "exploratory" | "action" | "other"
	Detail    string    `json:"detail"`         // command keyword for Bash, empty otherwise
	InputSize int       `json:"input_size"`     // rough byte count of tool input
}

// ExternalSummary is the per-tool-or-category rollup shown in session export.
type ExternalSummary struct {
	Tool     string `json:"tool"`
	Category string `json:"category"`
	Count    int    `json:"count"`
}

// postToolUsePayload is the JSON Claude Code pipes to PostToolUse hooks.
// Field names follow the Claude Code hook spec; unknown fields are ignored.
type postToolUsePayload struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	// We don't use tool_response — we only need the tool name and input
	// to classify the call.
}

// ParsePostToolUse reads the Claude Code PostToolUse hook payload from r
// and returns an ExternalRecord ready to append. Returns (zero, false)
// when the call should be skipped (myco MCP tools, unknown/empty input).
func ParsePostToolUse(r io.Reader, sessionID string) (ExternalRecord, bool) {
	b, err := io.ReadAll(r)
	if err != nil || len(b) == 0 {
		return ExternalRecord{}, false
	}
	var p postToolUsePayload
	if err := json.Unmarshal(b, &p); err != nil {
		return ExternalRecord{}, false
	}
	if p.ToolName == "" {
		return ExternalRecord{}, false
	}
	// Skip myco's own MCP calls — they're already in telemetry.jsonl.
	if strings.HasPrefix(p.ToolName, "mcp__") {
		return ExternalRecord{}, false
	}

	detail := extractDetail(p.ToolName, p.ToolInput)
	category := classifyTool(p.ToolName, detail)

	return ExternalRecord{
		Timestamp: time.Now(),
		SessionID: sessionID,
		ToolName:  p.ToolName,
		Category:  category,
		Detail:    detail,
		InputSize: len(p.ToolInput),
	}, true
}

// ClassifyTool returns (category, detail) for a tool call. Exported so
// tests can exercise the classification table without building a full
// hook payload.
func ClassifyTool(toolName, command string) (category, detail string) {
	detail = extractDetailFromCommand(toolName, command)
	category = classifyTool(toolName, detail)
	return
}

// AppendExternal writes one ExternalRecord to the per-session external
// log at path. The file is created if absent; writes are not
// concurrency-safe (the track command is a short-lived process, not the
// daemon — only one instance runs per hook invocation).
func AppendExternal(path string, rec ExternalRecord) error {
	if err := os.MkdirAll(pathDir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir external log: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open external log: %w", err)
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// SummarizeExternal streams the external JSONL at path and returns a
// per-tool count list, sorted by count descending. Returns (nil, nil)
// when the file doesn't exist (no fallback calls in this session).
func SummarizeExternal(path string) ([]ExternalSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open external log: %w", err)
	}
	defer f.Close()

	type key struct{ tool, category string }
	counts := map[key]int{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r ExternalRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		label := r.ToolName
		if r.Detail != "" {
			label = r.ToolName + "/" + r.Detail
		}
		counts[key{tool: label, category: r.Category}]++
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read external log: %w", err)
	}

	out := make([]ExternalSummary, 0, len(counts))
	for k, n := range counts {
		out = append(out, ExternalSummary{Tool: k.tool, Category: k.category, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tool < out[j].Tool
	})
	return out, nil
}

// ExternalPath returns the conventional path for a session's external log.
func ExternalPath(dir, sessionID string) string {
	return dir + "/session_" + sessionID + "_external.jsonl"
}

// TotalExploratory returns the count of exploratory (fallback) calls in
// the summary — the headline signal for the myco-vs-grep comparison.
func TotalExploratory(summaries []ExternalSummary) int {
	n := 0
	for _, s := range summaries {
		if s.Category == "exploratory" {
			n += s.Count
		}
	}
	return n
}

// ─── classification internals ─────────────────────────────────────────────────

// exploratoryBashCmds are the shell commands that indicate the agent is
// doing raw code exploration rather than taking an action. When an agent
// uses these instead of myco tools it's a signal that myco didn't cover
// the use case.
var exploratoryBashCmds = map[string]bool{
	"grep": true, "rg": true, "ripgrep": true,
	"find": true, "fd": true,
	"cat": true, "head": true, "tail": true,
	"less": true, "more": true,
	"awk": true, "sed": true,
	"wc": true, "sort": true, "uniq": true,
	"ls": true, "tree": true,
	"ag": true, // the_silver_searcher
}

func classifyTool(toolName, detail string) string {
	switch toolName {
	case "Bash":
		if exploratoryBashCmds[detail] {
			return "exploratory"
		}
		// Bash calls with non-exploratory commands (npm, go, git, etc.)
		// are still worth recording but as "action".
		return "action"
	case "Read":
		return "exploratory"
	case "WebSearch", "WebFetch":
		return "exploratory"
	case "Edit", "Write", "NotebookEdit":
		return "action"
	case "Agent":
		return "other"
	default:
		return "other"
	}
}

func extractDetail(toolName string, rawInput json.RawMessage) string {
	if toolName != "Bash" || len(rawInput) == 0 {
		return ""
	}
	var inp struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"` // some versions use "cmd"
	}
	if err := json.Unmarshal(rawInput, &inp); err != nil {
		return ""
	}
	cmd := inp.Command
	if cmd == "" {
		cmd = inp.Cmd
	}
	return extractDetailFromCommand(toolName, cmd)
}

func extractDetailFromCommand(toolName, command string) string {
	if toolName != "Bash" || command == "" {
		return ""
	}
	// First non-empty token in the command string is the executable.
	// Strip leading env-var assignments (FOO=bar cmd ...) and flags.
	fields := strings.Fields(command)
	for _, f := range fields {
		if strings.Contains(f, "=") {
			continue // skip VAR=value
		}
		// Strip path prefix (e.g. /usr/bin/grep → grep).
		parts := strings.Split(f, "/")
		return parts[len(parts)-1]
	}
	return ""
}

// pathDir is filepath.Dir without importing path/filepath in this file
// (filepath is already imported by the package's other files).
func pathDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "."
	}
	return p[:i]
}
