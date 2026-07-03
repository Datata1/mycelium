package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/languages"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/repo"
)

func newIndexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "index",
		Short: "Run a one-shot full index of the repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			ix, err := openIndex(rc)
			if err != nil {
				return err
			}
			defer ix.Close()

			reg := languages.Registry(rc.Cfg.Languages)
			w := repo.NewWalker(rc.Root, rc.Cfg.Include, rc.Cfg.Exclude, rc.Cfg.Index.MaxFileSizeKB)
			resolvers := languages.Resolvers(rc.Root, rc.Cfg.Languages, nil)
			wss, projFor, err := buildWorkspaces(ctx, rc, ix)
			if err != nil {
				return err
			}
			p := &pipeline.Pipeline{
				Index: ix, Registry: reg, Walker: w,
				Resolvers: resolvers, Workspaces: wss, FileProjectFor: projFor,
				Documents: buildDocumentRegistry(),
			}

			rep, err := p.RunOnce(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("scanned=%d changed=%d skipped=%d symbols=%d refs=%d duration=%s\n",
				rep.FilesScanned, rep.FilesChanged, rep.FilesSkipped, rep.Symbols, rep.Refs, rep.Duration)
			if len(rep.Errors) > 0 {
				fmt.Fprintf(os.Stderr, "errors (%d):\n", len(rep.Errors))
				for _, e := range rep.Errors {
					fmt.Fprintln(os.Stderr, " -", e)
				}
			}
			return nil
		},
	}
}
