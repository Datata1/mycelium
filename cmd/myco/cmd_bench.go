package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jdwiederstein/mycelium/internal/bench"
	"github.com/jdwiederstein/mycelium/internal/config"
	"github.com/jdwiederstein/mycelium/internal/ipc"
)

func newBenchCounterfactualCmd() *cobra.Command {
	var (
		driftThreshold float64
		format         string
		repoOverride   string
		language       string
		adaptive       bool
	)
	cmd := &cobra.Command{
		Use:   "bench-counterfactual",
		Short: "Calibrate the without-myco cost model against an indexed repo",
		Long: `Runs each myco tool against the running daemon and the equivalent
shell fallback (grep / wc -c / find), then compares the measured byte
ratio against the modelled multiplier in internal/telemetry/counterfactual.go.

Drift exceeding --drift-threshold (default 0.50 = 50%) on any tool
exits with status 1, so calibration regressions break CI loudly.

Two corpora are supported:
  - default (mycelium-tuned): hard-coded targets that exist in this
    repo. Use for re-validating the calibration against mycelium-self.
  - --adaptive: probes the daemon (list_files + get_file_outline)
    to pick representative (file, symbol) targets for any indexed
    repo. Use this when running against external repos via --repo.

When --repo is passed without --adaptive, the bench will likely show
ERR rows because mycelium-tuned targets won't exist in other repos.
A friendly error suggests --adaptive in that case.

--language selects the per-language multiplier override when one
is populated (see counterfactualModel.perLang).

Requires a running daemon (start with ` + "`myco daemon`" + `).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBenchCounterfactual(driftThreshold, format, repoOverride, language, adaptive)
		},
	}
	cmd.Flags().Float64Var(&driftThreshold, "drift-threshold", 0.5,
		"max allowed |measured-model|/model before exiting non-zero")
	cmd.Flags().StringVar(&format, "format", "table", "output format: table | json")
	cmd.Flags().StringVar(&repoOverride, "repo", "",
		"absolute path to a repo whose daemon socket should be used (default: cwd's repo)")
	cmd.Flags().StringVar(&language, "language", "",
		"dominant repo language (go, typescript, python, rust, …) — selects the per-language multiplier override when populated")
	cmd.Flags().BoolVar(&adaptive, "adaptive", false,
		"probe the daemon to pick representative targets dynamically (recommended for --repo against non-mycelium repos)")
	return cmd
}

func runBenchCounterfactual(driftThreshold float64, format, repoOverride, language string, adaptive bool) error {
	root := repoOverride
	socket := ""
	if root == "" {
		rc, err := loadRepoCtx()
		if err != nil {
			return err
		}
		root = rc.Root
		socket = rc.Cfg.Daemon.Socket
	} else {
		// When --repo is passed, build a thin context from the path —
		// don't run loadRepoCtx (which would still discover from cwd
		// and ignore the override). Default socket name matches the
		// out-of-box config.
		socket = config.Default().Daemon.Socket
	}

	client := ipc.NewClient(root + "/" + socket)
	if !client.IsReachable() {
		return fmt.Errorf("daemon not reachable at %s/%s — start it with `myco daemon` (in that repo)",
			root, socket)
	}

	// v4 T4: adaptive corpus probes the daemon for real targets in the
	// indexed repo. Falls back to the mycelium-tuned default with a clear
	// error if probing fails (e.g. empty index).
	var corpus bench.Corpus
	if adaptive {
		c, err := bench.BuildAdaptiveCorpus(client)
		if err != nil {
			return fmt.Errorf("--adaptive: %w (try without --adaptive on a mycelium repo)", err)
		}
		corpus = c
	} else {
		corpus = bench.MyceliumDefaultCorpus()
	}

	rows := bench.Run(client, root, corpus, language, driftThreshold)

	// When --repo points at an external repo without --adaptive, the static
	// corpus is likely to ERR on most rows. Help the user reach for
	// --adaptive instead.
	if !adaptive && repoOverride != "" && allRowsFailed(rows) {
		fmt.Fprintf(os.Stderr,
			"\nhint: every row failed against --repo %s — the default corpus is mycelium-tuned. Re-run with --adaptive to probe targets from the indexed repo.\n",
			repoOverride)
	}

	switch format {
	case "json":
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
	default:
		bench.PrintTable(rows, driftThreshold, corpus.Name, language)
	}

	for _, r := range rows {
		if !r.OK || r.Err != "" {
			return fmt.Errorf("calibration drift exceeded threshold (%.0f%%); see table above",
				driftThreshold*100)
		}
	}
	return nil
}

// allRowsFailed reports whether every bench row has an error — the "wrong
// corpus for this repo" signal used to suggest --adaptive.
func allRowsFailed(rows []bench.Row) bool {
	for _, r := range rows {
		if r.Err == "" {
			return false
		}
	}
	return len(rows) > 0
}
