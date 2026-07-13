package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/service"
	"github.com/datata1/mycelium/internal/telemetry"
)

// verifyGateTimeout bounds the whole Stop-hook run; a verifier that
// stalls a session teardown is worse than no verifier.
const verifyGateTimeout = 10 * time.Second

// verifyReasonMaxDanglers caps the call-site lines in the block reason
// (~200-token budget; the agent can run `myco check` for the rest).
const verifyReasonMaxDanglers = 5

// newSessionVerifyCmd is the opt-in Stop-hook gate (installed via
// `myco session hooks install --verify-gate`). It blocks a session from
// ending ONLY when `verify_changes` finds removed symbols with exact
// dangling references — high-confidence broken call sites. Everything
// else — warnings, a stale index, any internal error — lets the session
// end: an untrustworthy or obstructive gate kills adoption (same
// silent-on-error contract as `session prime`).
func newSessionVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "verify",
		Short:  "Stop-hook gate: block session end while changes break references (silent on any error)",
		Hidden: false,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data := telemetry.ParseHookStdin(os.Stdin)
			// A previous block already forced Claude to continue; let it
			// stop now rather than loop (Claude Code caps at 8 blocks
			// anyway, but well-behaved hooks yield on the signal).
			if data.StopHookActive {
				return nil
			}

			rc, err := loadRepoCtx()
			if err != nil {
				return nil
			}
			if _, err := os.Stat(rc.AbsIndexPath()); err != nil {
				return nil // no index — nothing trustworthy to gate on
			}

			ctx, cancel := context.WithTimeout(context.Background(), verifyGateTimeout)
			defer cancel()
			rep, err := callRead(ctx, rc, ipc.MethodVerifyChanges,
				ipc.VerifyChangesParams{Since: "HEAD"}, (*service.Service).VerifyChanges)
			if err != nil {
				return nil
			}
			if out := verifyGateOutput(rep); out != "" {
				fmt.Println(out)
			}
			return nil
		},
	}
}

// verifyGateOutput builds the Stop-hook JSON for a report, or "" when
// the session may end. Blocks ONLY on a removed_but_referenced FAIL —
// a stale index (infra problem, not the agent's code) and short-name
// warnings never block.
func verifyGateOutput(rep ipc.VerifyReport) string {
	var failed bool
	for _, c := range rep.Checks {
		if c.Name == "removed_but_referenced" && c.Level == "fail" {
			failed = true
			break
		}
	}
	if !failed {
		return ""
	}

	var lines []string
	shown := 0
	for _, rm := range rep.Removed {
		for _, d := range rm.Danglers {
			if !d.Exact {
				continue
			}
			if shown == verifyReasonMaxDanglers {
				break
			}
			lines = append(lines, fmt.Sprintf("  %s — still referenced from %s:%d", rm.Qualified, d.Path, d.Line))
			shown++
		}
	}
	reason := fmt.Sprintf(
		"myco check: removed symbol(s) are still referenced from files outside your changes:\n%s\nFix the call sites or restore the symbol(s). Run `myco check` for the full report.",
		strings.Join(lines, "\n"))

	b, err := json.Marshal(map[string]string{
		"decision": "block",
		"reason":   reason,
	})
	if err != nil {
		return ""
	}
	return string(b)
}
