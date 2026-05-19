// Command myco is the Mycelium CLI: a local repository knowledge base for
// AI coding agents. Three transports (MCP stdio, unix socket, HTTP loopback)
// share one dispatcher backed by a single SQLite file at .mycelium/index.db.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/jdwiederstein/mycelium/internal/config"
	"github.com/jdwiederstein/mycelium/internal/repo"
)

// version is set at release build time via -ldflags "-X main.version=v3.x.y".
// In dev builds it falls back to a "dev-<commit>[-dirty]" string derived
// from the VCS metadata Go embeds automatically since Go 1.18.
var version = "dev"

func init() {
	if version == "dev" {
		version = devVersion()
	}
}

// devVersion reads the git commit hash (and dirty flag) that Go embeds in
// every binary when built inside a git working tree. Returns "dev" when VCS
// info is unavailable (e.g. built from a source tarball with no .git dir).
func devVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var commit, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 8 {
				commit = s.Value[:8]
			} else {
				commit = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if commit != "" {
		return "dev-" + commit + dirty
	}
	return "dev"
}

func main() {
	root := &cobra.Command{
		Use:           "myco",
		Short:         "Mycelium: a local repo knowledge base for AI agents",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newInitCmd(),
		newUninstallCmd(),
		newDaemonCmd(),
		newMCPCmd(),
		newIndexCmd(),
		newQueryCmd(),
		newHookCmd(),
		newStatsCmd(),
		newDoctorCmd(),
		newReadCmd(),
		newSessionCmd(),
		newBenchCounterfactualCmd(),
		// v4 T6: top-level aliases for the two queries users reach for by
		// reflex. Both delegate to the existing query subcommand handlers.
		newTopLevelFindCmd(),
		newTopLevelSearchCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// repoCtx holds the repo root and loaded config for a single command
// invocation.
type repoCtx struct {
	Root string
	Cfg  config.Config
}

// loadRepoCtx resolves the repo root and loads config (with defaults when
// .mycelium.yml is absent), so every command has a consistent starting point.
func loadRepoCtx() (repoCtx, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return repoCtx{}, err
	}
	root, err := repo.DiscoverRoot(cwd)
	if err != nil {
		return repoCtx{}, err
	}
	cfgPath := root + "/" + config.DefaultPath
	cfg := config.Default()
	if _, err := os.Stat(cfgPath); err == nil {
		loaded, loadErr := config.Load(cfgPath)
		if loadErr != nil {
			return repoCtx{}, loadErr
		}
		cfg = loaded
	}
	return repoCtx{Root: root, Cfg: cfg}, nil
}
