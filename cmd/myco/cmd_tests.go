package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/service"
)

// newTestsCmd is the CLI face of select_tests. Default output is bare
// paths, one per line, so it pipes straight into a runner:
//
//	go test $(myco tests)
//	myco tests | xargs -r npx vitest run
func newTestsCmd() *cobra.Command {
	var since, project string
	var depth int
	var jsonOutput, dirs bool
	cmd := &cobra.Command{
		Use:   "tests",
		Short: "List the test files that exercise the current changes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			ctx := context.Background()
			res, err := callRead(ctx, rc, ipc.MethodSelectTests,
				ipc.SelectTestsParams{Since: since, Depth: depth, Project: project},
				(*service.Service).SelectTests)
			if err != nil {
				return err
			}
			if jsonOutput {
				b, err := json.MarshalIndent(res, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(b))
				return nil
			}
			if dirs {
				// Go's test runner takes packages, not files. Dedup the
				// parent dirs, ./-prefixed so `go test $(myco tests --dirs)`
				// resolves as package paths.
				seen := map[string]struct{}{}
				for _, tf := range res.TestFiles {
					d := "./" + path.Dir(tf.Path)
					if _, dup := seen[d]; dup {
						continue
					}
					seen[d] = struct{}{}
					fmt.Println(d)
				}
			} else {
				for _, tf := range res.TestFiles {
					fmt.Println(tf.Path)
				}
			}
			for _, n := range res.Notes {
				fmt.Fprintln(os.Stderr, "note: "+n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "git ref for the diff base (default HEAD: uncommitted work)")
	cmd.Flags().IntVar(&depth, "depth", 0, "inbound-closure walk depth (default 5, max 10)")
	cmd.Flags().StringVar(&project, "project", "", "restrict to one workspace project")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit the full result as JSON (paths, projects, distances)")
	cmd.Flags().BoolVar(&dirs, "dirs", false, "print unique ./-prefixed parent dirs instead of files (Go: `go test $(myco tests --dirs)`)")
	return cmd
}
