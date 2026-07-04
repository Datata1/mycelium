package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/config"
	"github.com/datata1/mycelium/internal/telemetry"
)

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Measure and export agent session telemetry (v2.6)",
	}
	cmd.AddCommand(
		newSessionStartCmd(),
		newSessionListCmd(),
		newSessionExportCmd(),
		newSessionCompareCmd(),
		newSessionAnnotateCmd(),
		newSessionTrackCmd(),
		newSessionTranscriptCmd(),
		newSessionHooksCmd(),
		newSessionPrimeCmd(),
	)
	return cmd
}

// transcriptFor resolves the Claude Code transcript path for a session:
// uses the stored TranscriptPath first, then falls back to the conventional
// ~/.claude/projects/<slug>/<claude_session_id>.jsonl derivation.
func transcriptFor(rep telemetry.SessionReport, repoRoot string) string {
	if rep.Session.TranscriptPath != "" {
		return rep.Session.TranscriptPath
	}
	return telemetry.TranscriptPathFromSessionID(repoRoot, rep.Session.ClaudeSessionID)
}

func sessionPaths(rc repoCtx) (jsonlPath, sessionFilePath, hookMetaDir string) {
	base := rc.AbsStateDir()
	jsonlPath = rc.Cfg.Telemetry.Path
	if jsonlPath == "" {
		jsonlPath = filepath.Join(base, "telemetry.jsonl")
	}
	sessionFilePath = filepath.Join(base, "current_session.json")
	hookMetaDir = base
	return
}

func newSessionStartCmd() *cobra.Command {
	var auto bool
	cmd := &cobra.Command{
		Use:   "start [name]",
		Short: "Begin a new named session (stamps all subsequent telemetry calls with its ID)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			if !rc.Cfg.Telemetry.Enabled {
				fmt.Fprintln(os.Stderr,
					"hint: telemetry.enabled is false — enable it in .mycelium.yml and restart the daemon.")
			}
			jsonlPath, sessionFilePath, _ := sessionPaths(rc)

			name := strings.Join(args, " ")
			var claudeSessionID, transcriptPath string
			if auto {
				hook := telemetry.ParseHookStdin(os.Stdin)
				if hook.Name != "" && name == "" {
					name = hook.Name
				}
				claudeSessionID = hook.ClaudeSessionID
				transcriptPath = hook.TranscriptPath
			}

			meta, err := telemetry.StartSession(jsonlPath, sessionFilePath, name, claudeSessionID, transcriptPath)
			if err != nil {
				return err
			}
			fmt.Printf("started  %s  %q\n", meta.ID, meta.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&auto, "auto", false,
		"read name hint from Claude Code hook stdin; for UserPromptSubmit hooks")
	return cmd
}

func newSessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List recorded sessions with call counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			jsonlPath, _, _ := sessionPaths(rc)
			reports, err := telemetry.ListSessions(jsonlPath)
			if err != nil {
				return err
			}
			if len(reports) == 0 {
				fmt.Fprintln(os.Stderr, "no sessions recorded (run `myco session start` first)")
				return nil
			}
			fmt.Printf("%-38s  %-20s  %-19s  %6s  %12s\n",
				"ID", "name", "started", "calls", "out_bytes")
			for _, r := range reports {
				fmt.Printf("%-38s  %-20s  %-19s  %6d  %12s\n",
					r.Session.ID,
					truncate(r.Session.Name, 20),
					r.Session.StartedAt.Format("2006-01-02 15:04:05"),
					r.TotalCalls,
					humanBytes(r.OutputBytes),
				)
			}
			return nil
		},
	}
}

func newSessionExportCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "export <session-id>",
		Short: "Render a full report for one session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			jsonlPath, _, hookMetaDir := sessionPaths(rc)
			rep, err := telemetry.AggregateSession(jsonlPath, args[0])
			if err != nil {
				return err
			}
			hook, hasHook := telemetry.ReadHookMeta(hookMetaDir, args[0])
			ext, _ := telemetry.SummarizeExternal(telemetry.ExternalPath(hookMetaDir, args[0]))
			ts, _ := telemetry.ParseTranscript(transcriptFor(rep, rc.Root))
			cpt := rc.Cfg.Telemetry.CharsPerToken
			if cpt <= 0 {
				cpt = config.DefaultCharsPerToken
			}
			cost := telemetry.ComputeSessionCost(rep.Summaries, ext, cpt)
			switch format {
			case "json":
				return printSessionJSON(rep, hook, hasHook, ext, ts, cost)
			case "markdown", "md":
				printSessionMarkdown(rep, hook, hasHook, ext, ts, cost)
			default:
				printSessionTable(rep, hook, hasHook, ext, ts, cost)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "table", "output format: table | json | markdown")
	return cmd
}

func newSessionCompareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compare <session-a> <session-b>",
		Short: "Side-by-side diff of two sessions",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			jsonlPath, _, hookMetaDir := sessionPaths(rc)
			a, err := telemetry.AggregateSession(jsonlPath, args[0])
			if err != nil {
				return fmt.Errorf("session A: %w", err)
			}
			b, err := telemetry.AggregateSession(jsonlPath, args[1])
			if err != nil {
				return fmt.Errorf("session B: %w", err)
			}
			hookA, _ := telemetry.ReadHookMeta(hookMetaDir, args[0])
			hookB, _ := telemetry.ReadHookMeta(hookMetaDir, args[1])
			extA, _ := telemetry.SummarizeExternal(telemetry.ExternalPath(hookMetaDir, args[0]))
			extB, _ := telemetry.SummarizeExternal(telemetry.ExternalPath(hookMetaDir, args[1]))
			tsA, _ := telemetry.ParseTranscript(transcriptFor(a, rc.Root))
			tsB, _ := telemetry.ParseTranscript(transcriptFor(b, rc.Root))
			printSessionCompare(a, b, hookA, hookB, extA, extB, tsA, tsB)
			return nil
		},
	}
}

// newSessionTrackCmd is called by the Claude Code PostToolUse hook after every
// tool call. It records non-myco tool uses so session export can show how often
// the agent fell back to grep/Read/etc. — the key adoption signal.
func newSessionTrackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "track",
		Short: "Record a non-myco tool call from a PostToolUse hook (reads JSON from stdin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			_, sessionFilePath, hookMetaDir := sessionPaths(rc)
			meta, ok := telemetry.LoadCurrentSession(sessionFilePath)
			if !ok {
				return nil
			}
			rec, ok := telemetry.ParsePostToolUse(os.Stdin, meta.ID)
			if !ok {
				return nil
			}
			extPath := telemetry.ExternalPath(hookMetaDir, meta.ID)
			return telemetry.AppendExternal(extPath, rec)
		},
	}
}

// newSessionTranscriptCmd renders the Claude Code conversation transcript
// linked to a session. Two modes:
//
//   - default ("fallbacks"): shows only the agent reasoning + tool call around
//     each fallback — the decision points where the agent chose not to use myco.
//   - --full: renders the complete conversation.
//
// The <session-id> arg may be omitted when --transcript <path> is given.
func newSessionTranscriptCmd() *cobra.Command {
	var full bool
	var explicitPath string
	cmd := &cobra.Command{
		Use:   "transcript [session-id]",
		Short: "Render the Claude conversation linked to a session (fallback decision points by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			jsonlPath, _, _ := sessionPaths(rc)

			tpath := explicitPath
			sessionID := ""
			if len(args) > 0 {
				sessionID = args[0]
			}
			if tpath == "" && sessionID == "" {
				return fmt.Errorf("provide a session ID, --transcript <path>, or both")
			}

			if tpath == "" {
				rep, err := telemetry.AggregateSession(jsonlPath, sessionID)
				if err != nil {
					return err
				}
				tpath = transcriptFor(rep, rc.Root)
				if tpath == "" {
					candidates := telemetry.DiscoverTranscripts(rc.Root, rep.Session.StartedAt)
					switch len(candidates) {
					case 0:
						fmt.Fprintf(os.Stderr,
							"No transcript found for session %s.\n"+
								"  Try one of:\n"+
								"  1. Pass the path explicitly: myco session transcript --transcript <path>\n"+
								"  2. Browse manually:           ls -lt ~/.claude/projects/%s/\n",
							sessionID, telemetry.ClaudeProjectSlug(rc.Root))
						return fmt.Errorf("transcript not found for session %s", sessionID)
					case 1:
						tpath = candidates[0]
						fmt.Fprintf(os.Stderr, "auto-discovered transcript: %s\n", tpath)
					default:
						fmt.Fprintf(os.Stderr, "Multiple transcripts found near session start time:\n")
						for i, c := range candidates {
							fmt.Fprintf(os.Stderr, "  [%d] %s\n", i+1, c)
						}
						fmt.Fprintf(os.Stderr, "Using newest. Pass --transcript <path> to override.\n")
						tpath = candidates[0]
					}
				}
			}

			events, err := telemetry.ParseTranscriptEvents(tpath)
			if err != nil {
				return fmt.Errorf("read transcript %s: %w", tpath, err)
			}
			if len(events) == 0 {
				return fmt.Errorf("transcript at %s contained no conversation turns", tpath)
			}
			if full {
				fmt.Print(telemetry.RenderTranscript(events))
			} else {
				fmt.Print(telemetry.RenderFallbackContext(events))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false,
		"render the complete conversation instead of just fallback decision points")
	cmd.Flags().StringVar(&explicitPath, "transcript", "",
		"explicit path to a Claude Code conversation JSONL file (no session ID required when set)")
	return cmd
}

// newSessionAnnotateCmd is called by the Claude Code Stop hook to attach token
// usage to the current session's sidecar file.
func newSessionAnnotateCmd() *cobra.Command {
	var (
		inputTokens  int
		outputTokens int
		sessionID    string
		fromStdin    bool
	)
	cmd := &cobra.Command{
		Use:   "annotate",
		Short: "Attach token/conversation metadata to the current session (for Stop hooks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			_, sessionFilePath, hookMetaDir := sessionPaths(rc)

			sid := sessionID
			if fromStdin {
				hook := telemetry.ParseHookStdin(os.Stdin)
				if hook.InputTokens > 0 {
					inputTokens = hook.InputTokens
				}
				if hook.OutputTokens > 0 {
					outputTokens = hook.OutputTokens
				}
			}
			if sid == "" {
				meta, ok := telemetry.LoadCurrentSession(sessionFilePath)
				if !ok {
					return fmt.Errorf("no active session; pass --session <id> explicitly")
				}
				sid = meta.ID
			}

			meta := telemetry.HookMeta{
				SessionID:    sid,
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			}
			if err := telemetry.WriteHookMeta(hookMetaDir, meta); err != nil {
				return err
			}
			fmt.Printf("annotated %s  in=%d out=%d\n", sid, inputTokens, outputTokens)
			return nil
		},
	}
	cmd.Flags().IntVar(&inputTokens, "input-tokens", 0, "input token count")
	cmd.Flags().IntVar(&outputTokens, "output-tokens", 0, "output token count")
	cmd.Flags().StringVar(&sessionID, "session", "", "session ID to annotate (default: current session)")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "parse token counts from Claude Code hook JSON on stdin")
	return cmd
}

// newSessionHooksCmd writes Claude Code hook entries to the project
// .claude/settings.json so sessions start and annotate automatically.
func newSessionHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Manage Claude Code hook integration for automatic session tracking",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Write UserPromptSubmit + Stop hooks to .claude/settings.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			binary, err := os.Executable()
			if err != nil {
				binary = "myco"
			}
			return installSessionHooks(rc.Root, binary)
		},
	})
	return cmd
}

// installSessionHooks reads the project .claude/settings.json (creating it if
// absent), merges the session hook entries, and writes it back. Uses
// settings.local.json so agents don't accidentally overwrite it.
func installSessionHooks(repoRoot, binary string) error {
	settingsPath := filepath.Join(repoRoot, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}

	raw := map[string]any{}
	if b, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(b, &raw)
	}

	startHook := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": binary + " session start --auto",
			},
		},
	}
	trackHook := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": binary + " session track",
			},
		},
	}
	annotateHook := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": binary + " session annotate --stdin",
			},
		},
	}
	primeHook := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": binary + " session prime",
			},
		},
	}

	hooks, _ := raw["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	hooks["UserPromptSubmit"] = mergeHookList(hooks["UserPromptSubmit"], startHook)
	hooks["PostToolUse"] = mergeHookList(hooks["PostToolUse"], trackHook)
	hooks["Stop"] = mergeHookList(hooks["Stop"], annotateHook)
	hooks["SessionStart"] = mergeHookList(hooks["SessionStart"], primeHook)
	raw["hooks"] = hooks

	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsPath, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", settingsPath)
	fmt.Println("  SessionStart     → myco session prime             (injects index snapshot + tool rules)")
	fmt.Println("  UserPromptSubmit → myco session start --auto    (new session per conversation)")
	fmt.Println("  PostToolUse      → myco session track            (records fallback grep/Read calls)")
	fmt.Println("  Stop             → myco session annotate --stdin (captures token counts)")
	fmt.Println()
	fmt.Println("Note: hooks are in settings.local.json — do not commit this file.")
	fmt.Println("Restart Claude Code for hooks to take effect.")
	return nil
}

// mergeHookList appends entry to the existing hook-event list only if the
// command string is not already present (deduplication by command string).
func mergeHookList(existing any, entry map[string]any) any {
	ourCmd := ""
	if hooks, ok := entry["hooks"].([]any); ok && len(hooks) > 0 {
		if h, ok := hooks[0].(map[string]any); ok {
			ourCmd, _ = h["command"].(string)
		}
	}

	var list []any
	switch v := existing.(type) {
	case []any:
		list = v
	case map[string]any:
		list = []any{v}
	}

	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if hs, ok := m["hooks"].([]any); ok {
			for _, h := range hs {
				if hm, ok := h.(map[string]any); ok {
					if cmd, _ := hm["command"].(string); cmd == ourCmd {
						return list
					}
				}
			}
		}
	}
	return append(list, entry)
}

// ─── session report renderers ─────────────────────────────────────────────────

func printSessionTable(rep telemetry.SessionReport, hook telemetry.HookMeta, hasHook bool, ext []telemetry.ExternalSummary, ts telemetry.TranscriptSummary, cost telemetry.SessionCost) {
	s := rep.Session
	fmt.Printf("session    %s\n", s.ID)
	fmt.Printf("name       %s\n", s.Name)
	fmt.Printf("started    %s\n", s.StartedAt.Format("2006-01-02 15:04:05"))
	if s.ClaudeSessionID != "" {
		fmt.Printf("claude_id  %s\n", s.ClaudeSessionID)
	}
	if ts.FirstUserMessage != "" {
		fmt.Printf("task       %s\n", truncate(ts.FirstUserMessage, 80))
	}
	fmt.Printf("myco_calls %d\n", rep.TotalCalls)
	fmt.Printf("in_bytes   %s\n", humanBytes(rep.InputBytes))
	fmt.Printf("out_bytes  %s\n", humanBytes(rep.OutputBytes))
	if rep.CallDuration > 0 {
		fmt.Printf("call_span  %s\n", rep.CallDuration.Round(time.Millisecond))
	}
	if hasHook && (hook.InputTokens > 0 || hook.OutputTokens > 0) {
		fmt.Printf("tokens     in=%d  out=%d\n", hook.InputTokens, hook.OutputTokens)
	}
	if ts.ToolCalls > 0 {
		fmt.Printf("turns      %d\n", ts.Turns)
		fmt.Printf("all_tools  %d  (myco=%d  edits=%d  agents=%d)\n",
			ts.ToolCalls, ts.MycoCallsFromTranscript, ts.Edits, ts.AgentSpawns)
		if ts.PlanModeUsed {
			fmt.Println("plan_mode  yes")
		}
	}
	if len(rep.Summaries) > 0 {
		fmt.Println()
		fmt.Println("── myco tools ──────────────────────────────────────────────────")
		fmt.Printf("%-22s  %6s  %6s  %12s  %8s\n", "tool", "calls", "ok", "out_bytes", "p50")
		for _, s := range rep.Summaries {
			fmt.Printf("%-22s  %6d  %6d  %12s  %8s\n",
				s.Tool, s.Count, s.OK, humanBytes(s.OutputBytes), s.P50Duration)
		}
	}
	if len(ext) > 0 {
		exploratory := telemetry.TotalExploratory(ext)
		fmt.Println()
		fmt.Printf("── fallback tools (non-myco)  exploratory=%d ────────────────────\n", exploratory)
		fmt.Printf("%-28s  %-12s  %6s  %12s\n", "tool", "category", "calls", "out_bytes")
		for _, e := range ext {
			fmt.Printf("%-28s  %-12s  %6d  %12s\n", e.Tool, e.Category, e.Count, humanBytes(int64(e.OutputBytes)))
		}
	} else {
		fmt.Println()
		fmt.Println("── fallback tools: none recorded ────────────────────────────────")
	}
	if cost.TotalBytes > 0 {
		fmt.Println()
		fmt.Printf("── cost estimate  (~%.1f chars/token) ───────────────────────────\n", cost.CharsPerToken)
		fmt.Printf("%-12s  %12s  %12s\n", "source", "bytes", "est. tokens")
		fmt.Printf("%-12s  %12s  %12s\n", "myco",
			humanBytes(cost.MycoInputBytes+cost.MycoOutputBytes),
			humanInt(cost.MycoTokens))
		fmt.Printf("%-12s  %12s  %12s\n", "fallback",
			humanBytes(cost.FallbackInputBytes+cost.FallbackOutputBytes),
			humanInt(cost.FallbackTokens))
		fmt.Printf("%-12s  %12s  %12s\n", "total",
			humanBytes(cost.TotalBytes), humanInt(cost.EstimatedTokens))

		if cost.WithoutMycoEstimateBytes > 0 {
			fmt.Println()
			fmt.Printf("── modelled savings  (estimate, see caveats) ───────────────────\n")
			fmt.Printf("%-22s  %12s  %12s\n", "scenario", "bytes", "est. tokens")
			fmt.Printf("%-22s  %12s  %12s\n", "with myco (actual)",
				humanBytes(cost.TotalBytes), humanInt(cost.EstimatedTokens))
			fmt.Printf("%-22s  %12s  %12s\n", "without myco (modelled)",
				humanBytes(cost.WithoutMycoEstimateBytes),
				humanInt(cost.WithoutMycoEstimateTokens))
			savingsPct := cost.SavingsRatio * 100
			fmt.Printf("%-22s  %12s  %12s  (%+.1f%%)\n", "estimated savings",
				humanBytes(cost.EstimatedSavingsBytes),
				humanInt(cost.EstimatedSavingsTokens),
				savingsPct)
			if mix := cost.CounterfactualQualityMix; len(mix) > 0 {
				fmt.Printf("quality mix: high=%d  medium=%d  low=%d\n",
					mix[telemetry.EstimateQualityHigh],
					mix[telemetry.EstimateQualityMedium],
					mix[telemetry.EstimateQualityLow])
			}
		}
	}
}

func printSessionMarkdown(rep telemetry.SessionReport, hook telemetry.HookMeta, hasHook bool, ext []telemetry.ExternalSummary, ts telemetry.TranscriptSummary, cost telemetry.SessionCost) {
	s := rep.Session
	fmt.Printf("## Session: %s\n\n", s.Name)
	if ts.FirstUserMessage != "" {
		fmt.Printf("> **Task:** %s\n\n", ts.FirstUserMessage)
	}
	fmt.Printf("| field | value |\n|---|---|\n")
	fmt.Printf("| ID | `%s` |\n", s.ID)
	if s.ClaudeSessionID != "" {
		fmt.Printf("| Claude session | `%s` |\n", s.ClaudeSessionID)
	}
	fmt.Printf("| Started | %s |\n", s.StartedAt.Format("2006-01-02 15:04:05"))
	if ts.Turns > 0 {
		fmt.Printf("| Conversation turns | %d |\n", ts.Turns)
		fmt.Printf("| All tool calls | %d |\n", ts.ToolCalls)
		fmt.Printf("| File edits | %d |\n", ts.Edits)
		if ts.AgentSpawns > 0 {
			fmt.Printf("| Agent spawns | %d |\n", ts.AgentSpawns)
		}
		fmt.Printf("| Plan mode | %v |\n", ts.PlanModeUsed)
	}
	fmt.Printf("| myco calls | %d |\n", rep.TotalCalls)
	fmt.Printf("| Input bytes | %s |\n", humanBytes(rep.InputBytes))
	fmt.Printf("| Output bytes | %s |\n", humanBytes(rep.OutputBytes))
	if rep.CallDuration > 0 {
		fmt.Printf("| Call span | %s |\n", rep.CallDuration.Round(time.Millisecond))
	}
	if hasHook && (hook.InputTokens > 0 || hook.OutputTokens > 0) {
		fmt.Printf("| Input tokens | %d |\n", hook.InputTokens)
		fmt.Printf("| Output tokens | %d |\n", hook.OutputTokens)
	}
	fmt.Printf("| Fallback exploratory calls | %d |\n", telemetry.TotalExploratory(ext))
	if len(rep.Summaries) > 0 {
		fmt.Printf("\n### myco tools\n\n")
		fmt.Printf("| tool | calls | ok | out_bytes | p50 |\n|---|---|---|---|---|\n")
		for _, s := range rep.Summaries {
			fmt.Printf("| %s | %d | %d | %s | %s |\n",
				s.Tool, s.Count, s.OK, humanBytes(s.OutputBytes), s.P50Duration)
		}
	}
	if len(ext) > 0 {
		fmt.Printf("\n### Fallback tools (non-myco)\n\n")
		fmt.Printf("| tool | category | calls | out_bytes |\n|---|---|---|---|\n")
		for _, e := range ext {
			fmt.Printf("| %s | %s | %d | %s |\n",
				e.Tool, e.Category, e.Count, humanBytes(int64(e.OutputBytes)))
		}
		extIn, extOut := telemetry.TotalExternalBytes(ext)
		fmt.Printf("\n**Fallback total bytes** — in: %s · out: %s\n",
			humanBytes(int64(extIn)), humanBytes(int64(extOut)))
	}
	if cost.TotalBytes > 0 {
		fmt.Printf("\n### Cost estimate\n\n")
		fmt.Printf("Estimated at **%.1f chars/token** (override via `telemetry.chars_per_token` in `.mycelium.yml`). Token numbers are directional — for trend-watching, not billing.\n\n", cost.CharsPerToken)
		fmt.Printf("| source | bytes (in + out) | est. tokens |\n|---|---|---|\n")
		fmt.Printf("| myco | %s | %s |\n",
			humanBytes(cost.MycoInputBytes+cost.MycoOutputBytes),
			humanInt(cost.MycoTokens))
		fmt.Printf("| fallback (Read/Bash/Edit/…) | %s | %s |\n",
			humanBytes(cost.FallbackInputBytes+cost.FallbackOutputBytes),
			humanInt(cost.FallbackTokens))
		fmt.Printf("| **total** | **%s** | **%s** |\n",
			humanBytes(cost.TotalBytes), humanInt(cost.EstimatedTokens))

		if cost.WithoutMycoEstimateBytes > 0 {
			fmt.Printf("\n#### Modelled savings vs. fallback-only\n\n")
			fmt.Printf("**Modelled, not measured.** Per-tool multipliers estimate what the equivalent `grep`/`Read`/`find` operation would have cost in bytes — actual fallback runs would re-traverse the filesystem and contend with editor/build, so we trade accuracy for a per-call estimate good enough to track adoption-cost trends.\n\n")
			fmt.Printf("| scenario | bytes | est. tokens |\n|---|---|---|\n")
			fmt.Printf("| with myco (actual) | %s | %s |\n",
				humanBytes(cost.TotalBytes), humanInt(cost.EstimatedTokens))
			fmt.Printf("| without myco (modelled) | %s | %s |\n",
				humanBytes(cost.WithoutMycoEstimateBytes),
				humanInt(cost.WithoutMycoEstimateTokens))
			savingsPct := cost.SavingsRatio * 100
			fmt.Printf("| **estimated savings** | **%s** | **%s** (%+.1f%%) |\n",
				humanBytes(cost.EstimatedSavingsBytes),
				humanInt(cost.EstimatedSavingsTokens),
				savingsPct)
			if mix := cost.CounterfactualQualityMix; len(mix) > 0 {
				fmt.Printf("\n_Estimate quality mix: %d high · %d medium · %d low. Low-quality estimates come from graph tools (`get_neighborhood`, `impact_analysis`, `critical_path`) where the fallback would be iterated grep+Read — hard to model from output bytes alone._\n",
					mix[telemetry.EstimateQualityHigh],
					mix[telemetry.EstimateQualityMedium],
					mix[telemetry.EstimateQualityLow])
			}
			if cost.EstimatedSavingsBytes < 0 {
				fmt.Printf("\n_Negative savings means myco's actual byte cost exceeded the modelled fallback cost — usually a sign the agent reached for myco where a single `grep` would have been cheaper, or used a graph tool the model under-counts. Worth digging into per-tool rows below._\n")
			}
		}

		if len(cost.PerTool) > 0 {
			fmt.Printf("\n#### Top contributors\n\n")
			fmt.Printf("| tool | source | calls | bytes | est. tokens | cf bytes |\n|---|---|---|---|---|---|\n")
			topN := len(cost.PerTool)
			if topN > 8 {
				topN = 8
			}
			for _, p := range cost.PerTool[:topN] {
				cfCell := "—"
				if p.CounterfactualBytes > 0 {
					cfCell = humanBytes(p.CounterfactualBytes)
					if p.EstimateQuality == telemetry.EstimateQualityLow {
						cfCell += " (low)"
					}
				}
				fmt.Printf("| %s | %s | %d | %s | %s | %s |\n",
					p.Tool, p.Source, p.Count,
					humanBytes(p.TotalBytes), humanInt(p.EstimatedTokens),
					cfCell)
			}
		}
	}
}

// humanInt formats n with thousands separators (e.g. "78,500").
func humanInt(n int64) string {
	if n < 0 {
		return fmt.Sprintf("%d", n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}

type sessionExportJSON struct {
	Session                  telemetry.SessionMeta        `json:"session"`
	TotalMycoCalls           int                          `json:"total_myco_calls"`
	InputBytes               int64                        `json:"input_bytes"`
	OutputBytes              int64                        `json:"output_bytes"`
	CallSpanMS               int64                        `json:"call_span_ms,omitempty"`
	InputTokens              int                          `json:"input_tokens,omitempty"`
	OutputTokens             int                          `json:"output_tokens,omitempty"`
	FallbackExploratoryTotal int                          `json:"fallback_exploratory_total"`
	MycoTools                []telemetry.Summary          `json:"myco_tools"`
	FallbackTools            []telemetry.ExternalSummary  `json:"fallback_tools"`
	Cost                     *telemetry.SessionCost       `json:"cost,omitempty"`
	Transcript               *telemetry.TranscriptSummary `json:"transcript,omitempty"`
}

func printSessionJSON(rep telemetry.SessionReport, hook telemetry.HookMeta, hasHook bool, ext []telemetry.ExternalSummary, ts telemetry.TranscriptSummary, cost telemetry.SessionCost) error {
	out := sessionExportJSON{
		Session:                  rep.Session,
		TotalMycoCalls:           rep.TotalCalls,
		InputBytes:               rep.InputBytes,
		OutputBytes:              rep.OutputBytes,
		MycoTools:                rep.Summaries,
		FallbackTools:            ext,
		FallbackExploratoryTotal: telemetry.TotalExploratory(ext),
	}
	if rep.CallDuration > 0 {
		out.CallSpanMS = rep.CallDuration.Milliseconds()
	}
	if hasHook {
		out.InputTokens = hook.InputTokens
		out.OutputTokens = hook.OutputTokens
	}
	if cost.TotalBytes > 0 {
		c := cost
		out.Cost = &c
	}
	if ts.ToolCalls > 0 || ts.Turns > 0 {
		out.Transcript = &ts
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func printSessionCompare(a, b telemetry.SessionReport, hookA, hookB telemetry.HookMeta, extA, extB []telemetry.ExternalSummary, tsA, tsB telemetry.TranscriptSummary) {
	nameA := truncate(a.Session.Name, 20)
	nameB := truncate(b.Session.Name, 20)
	fmt.Printf("%-26s  %20s  %20s  %10s\n", "metric", nameA, nameB, "delta")
	fmt.Println(strings.Repeat("─", 82))

	printCompareInt("myco_calls", int64(a.TotalCalls), int64(b.TotalCalls))
	printCompareBytes("myco_out_bytes", a.OutputBytes, b.OutputBytes)
	printCompareDur("myco_call_span", a.CallDuration, b.CallDuration)
	printCompareInt("fallback_exploratory", int64(telemetry.TotalExploratory(extA)), int64(telemetry.TotalExploratory(extB)))
	if tsA.ToolCalls > 0 || tsB.ToolCalls > 0 {
		printCompareInt("turns", int64(tsA.Turns), int64(tsB.Turns))
		printCompareInt("all_tool_calls", int64(tsA.ToolCalls), int64(tsB.ToolCalls))
		printCompareInt("edits", int64(tsA.Edits), int64(tsB.Edits))
		printCompareInt("agent_spawns", int64(tsA.AgentSpawns), int64(tsB.AgentSpawns))
	}

	if hookA.InputTokens > 0 || hookB.InputTokens > 0 {
		printCompareInt("input_tokens", int64(hookA.InputTokens), int64(hookB.InputTokens))
	}
	if hookA.OutputTokens > 0 || hookB.OutputTokens > 0 {
		printCompareInt("output_tokens", int64(hookA.OutputTokens), int64(hookB.OutputTokens))
	}

	allMyco := map[string]struct{}{}
	byToolA := map[string]telemetry.Summary{}
	byToolB := map[string]telemetry.Summary{}
	for _, s := range a.Summaries {
		allMyco[s.Tool] = struct{}{}
		byToolA[s.Tool] = s
	}
	for _, s := range b.Summaries {
		allMyco[s.Tool] = struct{}{}
		byToolB[s.Tool] = s
	}
	if len(allMyco) > 0 {
		fmt.Println()
		fmt.Printf("%-26s  %20s  %20s  %10s\n", "myco tool", "calls-A", "calls-B", "delta")
		fmt.Println(strings.Repeat("─", 82))
		tools := make([]string, 0, len(allMyco))
		for t := range allMyco {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		for _, t := range tools {
			ca := int64(byToolA[t].Count)
			cb := int64(byToolB[t].Count)
			d := cb - ca
			sign := "+"
			if d < 0 {
				sign = ""
			}
			fmt.Printf("%-26s  %20d  %20d  %s%d\n", t, ca, cb, sign, d)
		}
	}

	allFallback := map[string]struct{}{}
	byExtA := map[string]int{}
	byExtB := map[string]int{}
	for _, e := range extA {
		allFallback[e.Tool] = struct{}{}
		byExtA[e.Tool] = e.Count
	}
	for _, e := range extB {
		allFallback[e.Tool] = struct{}{}
		byExtB[e.Tool] = e.Count
	}
	if len(allFallback) > 0 {
		fmt.Println()
		fmt.Printf("%-26s  %20s  %20s  %10s\n", "fallback tool", "calls-A", "calls-B", "delta")
		fmt.Println(strings.Repeat("─", 82))
		tools := make([]string, 0, len(allFallback))
		for t := range allFallback {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		for _, t := range tools {
			ca := int64(byExtA[t])
			cb := int64(byExtB[t])
			d := cb - ca
			sign := "+"
			if d < 0 {
				sign = ""
			}
			fmt.Printf("%-26s  %20d  %20d  %s%d\n", t, ca, cb, sign, d)
		}
	}
}

func printCompareInt(label string, a, b int64) {
	d := b - a
	sign := "+"
	if d < 0 {
		sign = ""
	}
	fmt.Printf("%-22s  %20d  %20d  %s%d\n", label, a, b, sign, d)
}

func printCompareBytes(label string, a, b int64) {
	d := b - a
	sign := "+"
	if d < 0 {
		sign = ""
	}
	fmt.Printf("%-22s  %20s  %20s  %s%s\n", label, humanBytes(a), humanBytes(b), sign, humanBytes(abs64(d)))
}

func printCompareDur(label string, a, b time.Duration) {
	d := b - a
	sign := "+"
	if d < 0 {
		sign = ""
	}
	fmt.Printf("%-22s  %20s  %20s  %s%s\n", label,
		a.Round(time.Millisecond), b.Round(time.Millisecond), sign, d.Round(time.Millisecond))
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
