package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/service"
	"github.com/datata1/mycelium/internal/telemetry"
)

func newStatsCmd() *cobra.Command {
	var showTelemetry bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Print index status: languages, symbol counts, freshness",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			if showTelemetry {
				return runStatsTelemetry(rc)
			}
			s, err := callRead(ctx, rc, ipc.MethodStats, (any)(nil),
				func(svc *service.Service, ctx context.Context, _ any) (ipc.Stats, error) {
					return svc.Stats(ctx)
				})
			if err != nil {
				return err
			}
			fmt.Printf("files=%d symbols=%d refs=%d resolved=%d self_loops=%d unresolved_ratio=%.1f%% last_scan=%s\n",
				s.Files, s.Symbols, s.Refs, s.Resolved, s.SelfLoopCount, s.UnresolvedRatio()*100,
				s.LastScan.Format("2006-01-02 15:04:05"))
			fmt.Println("by kind:")
			for k, n := range s.ByKind {
				fmt.Printf("  %s: %d\n", k, n)
			}
			fmt.Println("by language:")
			for l, n := range s.ByLang {
				fmt.Printf("  %s: %d\n", l, n)
			}
			if len(s.UnresolvedByLanguage) > 0 {
				fmt.Println("unresolved refs by language:")
				for l, n := range s.UnresolvedByLanguage {
					fmt.Printf("  %s: %d / %d\n", l, n, s.TotalByLanguage[l])
				}
			}
			if len(s.ConfiguredProjects) > 0 {
				fmt.Println("configured projects:")
				for _, p := range s.ConfiguredProjects {
					fmt.Printf("  %s (root=%s): %d files\n", p.Name, p.Root, p.FileCount)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&showTelemetry, "telemetry", false,
		"aggregate the telemetry log instead of index stats (v2.2)")
	return cmd
}

// runStatsTelemetry renders a per-tool histogram from the telemetry JSONL
// log. Prints a friendly hint when the log is missing or empty.
func runStatsTelemetry(rc repoCtx) error {
	path := rc.Cfg.Telemetry.Path
	if path == "" {
		path = filepath.Join(rc.Root, ".mycelium", "telemetry.jsonl")
	}
	if !rc.Cfg.Telemetry.Enabled {
		fmt.Fprintf(os.Stderr,
			"hint: telemetry.enabled is false in .mycelium.yml — no calls have been recorded.\n"+
				"      enable it with `telemetry: { enabled: true }` and restart the daemon.\n")
	}
	summaries, err := telemetry.Aggregate(path)
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		fmt.Fprintf(os.Stderr, "hint: no records at %s yet.\n", path)
		return nil
	}
	fmt.Printf("%-22s  %6s  %6s  %12s  %12s  %8s  %8s\n",
		"tool", "calls", "ok", "in_total", "out_total", "p50", "p95")
	for _, s := range summaries {
		fmt.Printf("%-22s  %6d  %6d  %12s  %12s  %8s  %8s\n",
			s.Tool, s.Count, s.OK,
			humanBytes(s.InputBytes), humanBytes(s.OutputBytes),
			s.P50Duration, s.P95Duration)
	}
	return nil
}

// humanBytes formats n as a human-readable byte size (e.g. "1.2 MiB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
