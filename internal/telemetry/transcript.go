package telemetry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// TranscriptSummary is the evaluation-oriented extract of a Claude Code
// conversation transcript. It captures what happened at the agent level
// (turns, tool mix, edits, plan usage) so the session export can show
// myco's coverage relative to the full agent activity — without embedding
// the full conversation text.
type TranscriptSummary struct {
	// Turns is the number of user→assistant exchange pairs.
	Turns int `json:"turns"`
	// ToolCalls is the total count of all tool invocations (including myco MCP calls).
	ToolCalls int `json:"tool_calls"`
	// ToolBreakdown is the per-tool call count across the whole conversation.
	ToolBreakdown map[string]int `json:"tool_breakdown"`
	// Edits is the count of Edit/Write/NotebookEdit calls — a proxy for
	// how much code the agent actually changed.
	Edits int `json:"edits"`
	// AgentSpawns is the count of Agent tool calls (subagent delegations).
	AgentSpawns int `json:"agent_spawns"`
	// PlanModeUsed is true when an ExitPlanMode call was detected.
	PlanModeUsed bool `json:"plan_mode_used"`
	// FirstUserMessage is the first user turn's text, trimmed to 300 chars.
	// Gives enough context to identify what the session was trying to do.
	FirstUserMessage string `json:"first_user_message,omitempty"`
	// MycoCallsFromTranscript is the count of mcp__mycelium__* tool calls
	// observed in the transcript (cross-check with telemetry.jsonl).
	MycoCallsFromTranscript int `json:"myco_calls_from_transcript"`
}

// transcriptLine is a single JSON object in the Claude Code conversation JSONL.
// Only the fields needed for evaluation metrics are decoded.
type transcriptLine struct {
	Type    string            `json:"type"`
	Role    string            `json:"role"`
	Content json.RawMessage   `json:"content"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"` // tool_use: tool name
	Input json.RawMessage `json:"input"`
}

// ParseTranscript reads a Claude Code conversation JSONL file and returns
// an evaluation summary. Returns (zero, nil) when the file does not exist —
// callers treat that as "no transcript linked" rather than an error.
func ParseTranscript(path string) (TranscriptSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TranscriptSummary{}, nil
		}
		return TranscriptSummary{}, err
	}
	defer f.Close()
	return parseTranscriptReader(f)
}

// TranscriptPathFromSessionID derives the conventional Claude Code transcript
// path for a given conversation session ID and repo root. Claude Code stores
// transcripts at ~/.claude/projects/<slug>/<session_id>.jsonl where <slug>
// is the absolute repo path with / replaced by -.
//
// Returns "" when the home directory cannot be determined.
func TranscriptPathFromSessionID(repoRoot, claudeSessionID string) string {
	if claudeSessionID == "" || repoRoot == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	// Claude Code slug: strip leading slash, replace remaining / with -
	slug := strings.ReplaceAll(repoRoot, "/", "-")
	slug = strings.TrimPrefix(slug, "-")
	return filepath.Join(home, ".claude", "projects", slug, claudeSessionID+".jsonl")
}

func parseTranscriptReader(r io.Reader) (TranscriptSummary, error) {
	var s TranscriptSummary
	s.ToolBreakdown = map[string]int{}

	editTools := map[string]bool{"Edit": true, "Write": true, "NotebookEdit": true}
	lastRole := ""
	firstUserDone := false

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg transcriptLine
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		// Count conversation turns: each user→assistant pair is one turn.
		if msg.Role == "user" && lastRole == "assistant" {
			s.Turns++
		}
		if msg.Role != "" {
			lastRole = msg.Role
		}

		// Extract first user message text for task identification.
		if msg.Role == "user" && !firstUserDone {
			if text := extractFirstText(msg.Content); text != "" {
				s.FirstUserMessage = truncateStr(text, 300)
				firstUserDone = true
			}
		}

		// Parse content blocks for tool calls.
		var blocks []contentBlock
		// Content may be a string (older format) or an array of blocks.
		if len(msg.Content) > 0 && msg.Content[0] == '[' {
			_ = json.Unmarshal(msg.Content, &blocks)
		}

		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				name := b.Name
				s.ToolCalls++
				s.ToolBreakdown[name]++
				if editTools[name] {
					s.Edits++
				}
				if name == "Agent" {
					s.AgentSpawns++
				}
				if name == "ExitPlanMode" {
					s.PlanModeUsed = true
				}
				if strings.HasPrefix(name, "mcp__mycelium__") {
					s.MycoCallsFromTranscript++
				}
			}
		}
	}

	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return s, err
	}
	// If we only saw one role change, count it as 1 turn.
	if s.Turns == 0 && lastRole != "" {
		s.Turns = 1
	}
	return s, nil
}

// ─── human-readable renderers ─────────────────────────────────────────────────

// TranscriptEvent is one decoded moment in a conversation: a message, a tool
// call, or a tool result. Used by the renderers below.
type TranscriptEvent struct {
	Role       string // "user" | "assistant"
	Text       string // non-empty for text blocks
	ToolName   string // non-empty for tool_use blocks
	ToolInput  string // JSON-formatted input for tool_use
	ToolResult string // non-empty for tool_result blocks
	IsMCO      bool   // true when ToolName starts with mcp__mycelium__
	IsFallback bool   // true when tool is an exploratory non-myco call
}

// ParseTranscriptEvents decodes the conversation JSONL into a flat event
// slice. This is the foundation for both the full render and the filtered
// fallback-only render.
func ParseTranscriptEvents(path string) ([]TranscriptEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []TranscriptEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg transcriptLine
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		role := msg.Role

		// String content (some older transcript formats)
		if len(msg.Content) > 0 && msg.Content[0] == '"' {
			var text string
			if json.Unmarshal(msg.Content, &text) == nil && text != "" {
				events = append(events, TranscriptEvent{Role: role, Text: text})
			}
			continue
		}

		var blocks []contentBlock
		if len(msg.Content) > 0 && msg.Content[0] == '[' {
			_ = json.Unmarshal(msg.Content, &blocks)
		}

		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					events = append(events, TranscriptEvent{Role: role, Text: b.Text})
				}
			case "tool_use":
				inp := "{}"
				if len(b.Input) > 0 {
					inp = string(b.Input)
				}
				isMCO := strings.HasPrefix(b.Name, "mcp__mycelium__")
				isFallback := isFallbackTool(b.Name, inp)
				events = append(events, TranscriptEvent{
					Role:       role,
					ToolName:   b.Name,
					ToolInput:  inp,
					IsMCO:      isMCO,
					IsFallback: isFallback,
				})
			case "tool_result":
				var resultText string
				switch {
				case len(b.Input) > 0 && b.Input[0] == '"':
					_ = json.Unmarshal(b.Input, &resultText)
				default:
					// content field holds the result
					var res struct {
						Content any `json:"content"`
					}
					if json.Unmarshal(line, &res) == nil {
						switch v := res.Content.(type) {
						case string:
							resultText = v
						case []any:
							var parts []string
							for _, item := range v {
								if m, ok := item.(map[string]any); ok {
									if t, ok := m["text"].(string); ok {
										parts = append(parts, t)
									}
								}
							}
							resultText = strings.Join(parts, "\n")
						}
					}
				}
				if resultText == "" {
					// last resort: raw JSON
					resultText = string(b.Input)
				}
				events = append(events, TranscriptEvent{
					Role:       role,
					ToolResult: truncateStr(resultText, 400),
				})
			}
		}
	}
	return events, sc.Err()
}

// isFallbackTool returns true when the tool call looks like raw exploration
// that myco should have covered.
func isFallbackTool(name, input string) bool {
	switch name {
	case "Read", "WebSearch", "WebFetch":
		return true
	case "Bash":
		for _, cmd := range []string{"grep", "rg", "find", "fd", "cat", "head", "tail", "ls", "tree", "ag", "wc"} {
			if strings.Contains(input, `"`+cmd) || strings.Contains(input, " "+cmd+" ") ||
				strings.HasPrefix(input, cmd+" ") {
				return true
			}
		}
	}
	return false
}

// RenderTranscript formats the full conversation as Markdown — equivalent to
// the Python extract_chat.py script but produced natively by myco.
func RenderTranscript(events []TranscriptEvent) string {
	var sb strings.Builder
	for _, e := range events {
		switch {
		case e.ToolName != "":
			label := e.ToolName
			if e.IsMCO {
				label = "🔍 myco/" + strings.TrimPrefix(e.ToolName, "mcp__mycelium__")
			} else if e.IsFallback {
				label = "⚠️  fallback/" + e.ToolName
			}
			sb.WriteString("\n**Tool:** `" + label + "`\n```json\n")
			sb.WriteString(e.ToolInput)
			sb.WriteString("\n```\n")
		case e.ToolResult != "":
			sb.WriteString("\n> **Result:** " + strings.ReplaceAll(e.ToolResult, "\n", "\n> ") + "\n")
		case e.Text != "":
			if e.Role == "user" {
				sb.WriteString("\n---\n**User:** " + e.Text + "\n")
			} else {
				sb.WriteString("\n" + e.Text + "\n")
			}
		}
	}
	return sb.String()
}

// RenderFallbackContext returns only the events around fallback tool calls:
// the assistant message immediately before the fallback (shows the reasoning),
// the fallback call itself, and its result. This makes it easy to see exactly
// when and why the agent gave up on myco.
func RenderFallbackContext(events []TranscriptEvent) string {
	var sb strings.Builder
	sb.WriteString("# Fallback decision points\n\n")
	sb.WriteString("Each section shows the agent text immediately before a fallback tool call,\n")
	sb.WriteString("the fallback call, and its result. This is where myco wasn't sufficient.\n\n")

	n := 0
	for i, e := range events {
		if !e.IsFallback {
			continue
		}
		n++
		sb.WriteString("---\n\n")
		sb.WriteString("## Fallback #" + itoa(n) + " — `" + e.ToolName + "`\n\n")

		// Look back up to 3 events for the last assistant text (the reasoning).
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			if events[j].Text != "" && events[j].Role == "assistant" {
				sb.WriteString("**Agent reasoning before fallback:**\n\n")
				sb.WriteString("> " + strings.ReplaceAll(truncateStr(events[j].Text, 600), "\n", "\n> ") + "\n\n")
				break
			}
		}

		sb.WriteString("**Fallback call:**\n```json\n" + e.ToolInput + "\n```\n\n")

		// Show the result if the next event is a tool_result.
		if i+1 < len(events) && events[i+1].ToolResult != "" {
			sb.WriteString("**Result:**\n> " + strings.ReplaceAll(events[i+1].ToolResult, "\n", "\n> ") + "\n\n")
		}
	}
	if n == 0 {
		sb.WriteString("No fallback calls detected.\n")
	}
	return sb.String()
}

func itoa(n int) string {
	return strings.TrimSpace(strings.ReplaceAll(fmt.Sprintf("%4d", n), " ", ""))
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func extractFirstText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Array of blocks
	if raw[0] == '[' {
		var blocks []contentBlock
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return ""
		}
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
		return ""
	}
	// Plain string
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	return ""
}
