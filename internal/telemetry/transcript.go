package telemetry

import (
	"bufio"
	"encoding/json"
	"errors"
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
