package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/mcp/render"
	"github.com/datata1/mycelium/internal/service"
)

// newCheckCmd is the CLI face of verify_changes: a structural smoke
// test over the working tree (or a branch, via --since). Exit codes
// follow the doctor convention: 0 pass, 1 warn, 2 fail.
func newCheckCmd() *cobra.Command {
	var since string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Structural smoke test: did the current changes break references elsewhere?",
		Long: `Verifies the working tree (default) or a branch (--since <ref>) against
the index: symbols removed or renamed in the changed files that are
still referenced from files outside the change set are broken call
sites — caught in milliseconds, before compiling or running tests.
Checks named references only; it complements the compiler, it does not
replace it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			ctx := context.Background()
			rep, err := callRead(ctx, rc, ipc.MethodVerifyChanges,
				ipc.VerifyChangesParams{Since: since}, (*service.Service).VerifyChanges)
			if err != nil {
				return err
			}
			if jsonOutput {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(b))
			} else {
				raw, err := json.Marshal(rep)
				if err != nil {
					return err
				}
				// Same renderer the MCP surface uses — CLI and agent
				// output cannot drift.
				fmt.Print(render.Verify(raw))
			}
			if code := rep.ExitCode(); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "git ref for the diff base (default HEAD: verify uncommitted work)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit the full report as JSON (for CI / loops)")
	return cmd
}
