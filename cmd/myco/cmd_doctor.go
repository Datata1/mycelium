package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/doctor"
	"github.com/datata1/mycelium/internal/languages"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/repo"
	"github.com/datata1/mycelium/internal/service"
)

func newDoctorCmd() *cobra.Command {
	var (
		jsonOutput bool
		window     time.Duration
		noAdoption bool
		deep       bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run health checks on the index; exit 0/1/2 on pass/warn/fail",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			// doctor is read-only; direct DB open is fine alongside the
			// daemon because SQLite WAL allows concurrent readers.
			ix, err := openIndex(rc)
			if err != nil {
				return err
			}
			defer ix.Close()

			th := doctor.ThresholdsFromConfig(rc.Cfg)
			if window > 0 {
				th.AdoptionWindow = window
			}
			r := service.NewReadOnly(ix, rc.Root, nil).Reader()
			rep, err := doctor.Run(ctx, r, th, rc.Root, rc.AbsStateDir())
			if err != nil {
				return err
			}
			if deep {
				walked, err := deepWalkPaths(rc)
				if err != nil {
					return err
				}
				rows, err := r.AllFilePaths(ctx)
				if err != nil {
					return err
				}
				indexed := make([]string, len(rows))
				for i, row := range rows {
					indexed[i] = row.Path
				}
				rep.Add(doctor.DeepFreshness(walked, indexed, 5))
			}
			if noAdoption {
				rep.Adoption = nil
			}
			if jsonOutput {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(b))
			} else {
				printDoctorReport(rep)
			}
			// Adoption findings never affect exit code (informational
			// only, per v4 B2). ExitCode reads from Summary which only
			// counts the regular Checks.
			if code := rep.ExitCode(); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit the full report as JSON (for CI)")
	cmd.Flags().DurationVar(&window, "window", 0,
		"adoption-health window (e.g. 24h, 168h). 0 uses the configured default (7d)")
	cmd.Flags().BoolVar(&noAdoption, "no-adoption", false,
		"suppress the adoption-health section (v4 B2)")
	cmd.Flags().BoolVar(&deep, "deep", false,
		"re-walk the tree and diff it against the index (exact freshness check; slower)")
	return cmd
}

// deepWalkPaths builds the same walker set the daemon indexes with —
// but without touching the DB (no project upserts; WalkedPaths ignores
// project ids) — and returns the walked rel-path union.
func deepWalkPaths(rc repoCtx) (map[string]struct{}, error) {
	p := &pipeline.Pipeline{
		Registry:  languages.Registry(rc.Cfg.Languages),
		Walker:    repo.NewWalker(rc.Root, rc.Cfg.Include, rc.Cfg.Exclude, rc.Cfg.Index.MaxFileSizeKB),
		Documents: buildDocumentRegistry(),
	}
	for _, pc := range rc.Cfg.Projects {
		include := pc.Include
		if len(include) == 0 {
			include = rc.Cfg.Include
		}
		exclude := pc.Exclude
		if len(exclude) == 0 {
			exclude = rc.Cfg.Exclude
		}
		p.Workspaces = append(p.Workspaces, pipeline.Workspace{
			Walker: repo.NewWalker(rc.Root+"/"+pc.Root, include, exclude, rc.Cfg.Index.MaxFileSizeKB),
		})
	}
	return p.WalkedPaths()
}

func printDoctorReport(rep doctor.Report) {
	for _, c := range rep.Checks {
		marker := "  ok "
		switch c.Level {
		case doctor.LevelWarn:
			marker = "warn"
		case doctor.LevelFail:
			marker = "FAIL"
		}
		fmt.Printf("[%s] %-24s %s\n", marker, c.Name, c.Message)
	}
	fmt.Printf("\nsummary: %d pass, %d warn, %d fail\n",
		rep.Summary.Pass, rep.Summary.Warn, rep.Summary.Fail)
	if len(rep.Adoption) > 0 {
		fmt.Println("\nadoption (informational, never gates CI):")
		for _, a := range rep.Adoption {
			marker := "  ok "
			switch a.Level {
			case doctor.AdoptionLevelWarn:
				marker = "warn"
			case doctor.AdoptionLevelInfo:
				marker = "info"
			}
			fmt.Printf("[%s] %-26s %s\n", marker, a.Mode, a.Message)
			if a.Hint != "" {
				fmt.Printf("       hint: %s\n", a.Hint)
			}
		}
	}
}
