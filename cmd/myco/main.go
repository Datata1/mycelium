// Command myco is the Mycelium CLI: a local repository knowledge base for
// AI coding agents. Three transports (MCP stdio, unix socket, HTTP loopback)
// share one dispatcher backed by a single SQLite file at .mycelium/index.db.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/config"
	"github.com/datata1/mycelium/internal/repo"
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

// loadRepoCtx resolves the repo root and loads config using a two-tier merge:
//  1. Start from hard-coded defaults.
//  2. Apply user-level config (~/.config/myco/config.yml) when present —
//     lets users set preferred languages, telemetry, etc. without committing
//     any file to the repo.
//  3. Apply repo-level .mycelium.yml when present — always wins over the
//     user config.
func loadRepoCtx() (repoCtx, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return repoCtx{}, err
	}
	root, err := repo.DiscoverRoot(cwd)
	if err != nil {
		return repoCtx{}, err
	}

	cfg := config.Default()

	// Tier 1: user-level config (best-effort; parse errors are non-fatal).
	if userPath, uerr := config.UserConfigPath(); uerr == nil {
		if _, serr := os.Stat(userPath); serr == nil {
			if u, lerr := config.LoadUser(userPath); lerr == nil {
				cfg = config.ApplyUserConfig(cfg, u)
			} else {
				fmt.Fprintf(os.Stderr, "[myco] warning: user config unreadable (%v) — using defaults\n", lerr)
			}
		}
	}

	// Tier 2: repo-level config (wins over user config).
	cfgPath := root + "/" + config.DefaultPath
	if _, err := os.Stat(cfgPath); err == nil {
		loaded, loadErr := config.Load(cfgPath)
		if loadErr != nil {
			return repoCtx{}, loadErr
		}
		cfg = loaded
	}

	return repoCtx{Root: root, Cfg: cfg}, nil
}

// AbsIndexPath resolves cfg.Index.Path against the repo root, expanding ~/
// prefixes and honouring absolute paths so the index can live outside the repo.
func (rc repoCtx) AbsIndexPath() string {
	return resolvePath(rc.Root, rc.Cfg.Index.Path, config.DefaultIndexPath)
}

// AbsSocketPath resolves cfg.Daemon.Socket against the repo root, expanding ~/
// prefixes and honouring absolute paths.
func (rc repoCtx) AbsSocketPath() string {
	return resolvePath(rc.Root, rc.Cfg.Daemon.Socket, config.DefaultSocket)
}

// AbsStateDir returns the directory that holds the index, socket, PID file,
// and telemetry log. It is the parent of AbsIndexPath.
func (rc repoCtx) AbsStateDir() string {
	return filepath.Dir(rc.AbsIndexPath())
}

// resolvePath resolves a configured path value to an absolute filesystem path.
//   - Empty string → base/defaultRel.
//   - "~/" prefix  → $HOME/<rest>.
//   - Absolute     → used as-is.
//   - Otherwise    → base/configured (repo-relative).
func resolvePath(base, configured, defaultRel string) string {
	if configured == "" {
		return filepath.Join(base, defaultRel)
	}
	if strings.HasPrefix(configured, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(base, configured)
		}
		return filepath.Join(home, configured[2:])
	}
	if filepath.IsAbs(configured) {
		return configured
	}
	return filepath.Join(base, configured)
}
