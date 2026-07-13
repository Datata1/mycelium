package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/service"
)

// newSessionPrimeCmd is the SessionStart hook body: it emits a compact
// additionalContext block priming the agent with live index stats and
// the tool-choice rules. This is the dynamic half of priming — CLAUDE.md
// carries the durable rules, this proves the index is alive right now.
//
// Failure contract: ANY problem (no repo, no index, empty index) prints
// nothing and exits 0. A hook that breaks session startup kills adoption
// of the whole product; a missing context block costs one nudge.
func newSessionPrimeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prime",
		Short: "Emit SessionStart hook context: index snapshot + tool rules (silent on any error)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			rc, err := loadRepoCtx()
			if err != nil {
				return nil
			}
			// Don't let the daemon-down fallback create an empty index
			// as a side effect — no index means nothing to prime with.
			if _, err := os.Stat(rc.AbsIndexPath()); err != nil {
				return nil
			}
			s, err := callRead(ctx, rc, ipc.MethodStats, (any)(nil),
				func(svc *service.Service, ctx context.Context, _ any) (ipc.Stats, error) {
					return svc.Stats(ctx)
				})
			if err != nil {
				return nil
			}
			text, ok := primeContext(s, time.Now())
			if !ok {
				return nil
			}
			out, err := json.Marshal(map[string]any{
				"hookSpecificOutput": map[string]any{
					"hookEventName":     "SessionStart",
					"additionalContext": text,
				},
			})
			if err != nil {
				return nil
			}
			fmt.Println(string(out))
			return nil
		},
	}
}

// primeContext renders the additionalContext text from an index
// snapshot. ok=false means there is nothing trustworthy to prime with
// (empty index). Budget: the whole block must stay under ~250 tokens —
// it is injected into every session.
func primeContext(s ipc.Stats, now time.Time) (string, bool) {
	if s.Files == 0 {
		return "", false
	}
	resolvedPct := int((1 - s.UnresolvedRatio()) * 100)

	// Prefer the reconcile timestamp: LastScan only moves on content
	// changes, so it understates freshness after a no-op reconcile.
	scanTime := s.LastFullScan
	if scanTime.IsZero() {
		scanTime = s.LastScan
	}
	var b strings.Builder
	fmt.Fprintf(&b, "myco (MCP) is indexing this repo: %d files (%s), %d symbols, refs %d%% resolved%s. ",
		s.Files, primeLangs(s.ByLang), s.Symbols, resolvedPct, primeAge(scanTime, now))
	b.WriteString("Rules: identifier → find_symbol (never search_lexical); " +
		"callers → get_references; read a file → read_focused(path, focus=...); " +
		"orientation → get_file_outline / get_file_summary; " +
		"blast radius → impact_analysis; document keys (i18n, deps) → find_document_key; " +
		"after edits & before declaring done → verify_changes; " +
		"which tests to run → select_tests. " +
		"search_lexical is ONLY for literal strings/regex. " +
		"Pass returned path+project values verbatim — never prepend the repo root.")
	return b.String(), true
}

// primeLangs renders "go 155, typescript 36" from the by-language map,
// largest first; the unnamed document bucket ("") is skipped.
func primeLangs(byLang map[string]int) string {
	type lc struct {
		lang string
		n    int
	}
	var ls []lc
	for lang, n := range byLang {
		if lang == "" || n == 0 {
			continue
		}
		ls = append(ls, lc{lang, n})
	}
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].n != ls[j].n {
			return ls[i].n > ls[j].n
		}
		return ls[i].lang < ls[j].lang
	})
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = fmt.Sprintf("%s %d", l.lang, l.n)
	}
	if len(parts) == 0 {
		return "no languages"
	}
	return strings.Join(parts, ", ")
}

// primeAge renders ", last scan 2m ago" — or nothing when the scan time
// is unknown. Included so a stale index is visible in the context block
// rather than silently vouched for.
func primeAge(lastScan time.Time, now time.Time) string {
	if lastScan.IsZero() {
		return ""
	}
	age := now.Sub(lastScan)
	switch {
	case age < time.Minute:
		return ", last scan just now"
	case age < time.Hour:
		return fmt.Sprintf(", last scan %dm ago", int(age.Minutes()))
	case age < 48*time.Hour:
		return fmt.Sprintf(", last scan %dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf(", last scan %dd ago", int(age.Hours()/24))
	}
}
