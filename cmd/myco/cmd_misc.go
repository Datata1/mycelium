package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/hook"
	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/mcp"
	"github.com/datata1/mycelium/internal/repo"
	"github.com/datata1/mycelium/internal/service"
	"github.com/datata1/mycelium/internal/wizard"
)

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Git hook integrations",
	}
	for _, name := range hook.ManagedHooks {
		cmd.AddCommand(&cobra.Command{
			Use:   name,
			Short: fmt.Sprintf("Reconcile the index (invoked by .git/hooks/%s)", name),
			// Git passes hook-specific args (post-checkout: <old> <new>
			// <flag>); every hook maps to the same full reconcile, so
			// they are accepted and ignored.
			Args: cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer cancel()
				rc, err := loadRepoCtx()
				if err != nil {
					return err
				}
				return hook.Run(ctx, rc.AbsSocketPath())
			},
		})
	}
	return cmd
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the MCP protocol over stdio (spawned by Claude Code, Cursor, etc.)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			client := ipc.NewClient(rc.AbsSocketPath())
			if !client.IsReachable() {
				return fmt.Errorf("daemon is not running at %s — start it with `myco daemon &`", rc.AbsSocketPath())
			}
			srv := &mcp.Server{In: os.Stdin, Out: os.Stdout, Client: client, Version: version}
			return srv.Run(ctx)
		},
	}
}

// newReadCmd is the v2.4 `myco read` subcommand: it returns a single indexed
// file with non-focus-matching symbols collapsed to one-line markers. Empty
// --focus returns the file in full.
func newReadCmd() *cobra.Command {
	var (
		focus   string
		showHdr bool
	)
	cmd := &cobra.Command{
		Use:   "read <path>",
		Short: "Read one indexed file with non-matching symbols collapsed (v2.4)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			fr, err := callRead(cmd.Context(), rc, ipc.MethodReadFocused,
				ipc.ReadFocusedParams{Path: args[0], Focus: focus},
				(*service.Service).ReadFocused)
			if err != nil {
				return err
			}
			if showHdr {
				fmt.Fprintf(os.Stderr,
					"# %s  focus=%q  expanded=%d/%d  bytes=%d/%d\n",
					fr.Path, fr.Focus,
					fr.Stats.ExpandedSymbols, fr.Stats.TotalSymbols,
					fr.Stats.ReturnedBytes, fr.Stats.OriginalBytes,
				)
			}
			fmt.Print(fr.Content)
			if fr.Hint != "" {
				fmt.Fprintf(os.Stderr, "\n# %s\n", fr.Hint)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&focus, "focus", "", "lexical focus hint; empty returns the no-focus preview (outline + first 50 lines + hint, not the full file — see read_focused docs)")
	cmd.Flags().BoolVar(&showHdr, "stats", false, "print collapse stats to stderr")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	var (
		yes        bool
		dryRun     bool
		keepBinary bool
		purge      bool
	)
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove myco binaries, project state, and agent client integration",
		Long: `Reverse of myco init. Walks the same components in reverse, asks Y/N
for each, and removes what init wrote: session hooks in
.claude/settings.local.json, the post-commit git hook (restoring any
.mycelium-backup), the mycelium entry in ~/.claude.json, the .mycelium/
index directory, and finally the myco binaries on PATH.

The currently-running binary deletes itself last; on Unix the kernel
keeps the inode alive until exit, so this completes cleanly.

Flags let you stay non-interactive (-y), preview without changes
(--dry-run), keep the binary but unwire project state (--keep-binary),
or also remove the .mycelium.yml config (--purge).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUninstall(yes, dryRun, keepBinary, purge)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept all defaults without prompting")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be removed, change nothing")
	cmd.Flags().BoolVar(&keepBinary, "keep-binary", false, "skip removing the myco executable(s); only unwire project state")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove .mycelium.yml config (default keeps user config)")
	return cmd
}

func runUninstall(yes, dryRun, keepBinary, purge bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := repo.DiscoverRoot(cwd)
	inRepo := err == nil
	if !inRepo {
		root = cwd
	}

	runningBinary, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(runningBinary); err == nil {
		runningBinary = resolved
	}

	fmt.Printf("Mycelium uninstall  ·  %s\n", root)
	fmt.Println(strings.Repeat("─", 54))
	if dryRun {
		fmt.Println("DRY RUN — no changes will be written.")
	}

	// ── Step 1: session tracking hooks ───────────────────────────────
	if inRepo {
		wizard.Step("Session tracking hooks…")
		settingsPath := filepath.Join(root, ".claude", "settings.local.json")
		if _, err := os.Stat(settingsPath); err == nil {
			if wizard.YN(fmt.Sprintf("  Strip myco hook entries from %s?", settingsPath), true, yes) {
				if dryRun {
					wizard.Skip("(dry-run) would strip myco hook entries")
				} else {
					removed, err := uninstallSessionHooks(root)
					if err != nil {
						wizard.Warn("session hooks: " + err.Error())
					} else if removed {
						wizard.Done("stripped myco hook entries from settings.local.json")
					} else {
						wizard.Skip("no myco hook entries found")
					}
				}
			}
		} else {
			wizard.Skip(".claude/settings.local.json not found")
		}
	}

	// ── Step 2: git hooks ────────────────────────────────────────────
	if inRepo {
		wizard.Step("Git hooks…")
		anyPresent := false
		for _, name := range hook.ManagedHooks {
			if _, err := os.Stat(filepath.Join(root, ".git", "hooks", name)); err == nil {
				anyPresent = true
				break
			}
		}
		if anyPresent {
			if wizard.YN("  Remove mycelium git hooks (or restore .mycelium-backup)?", true, yes) {
				if dryRun {
					wizard.Skip("(dry-run) would uninstall mycelium hooks")
				} else {
					removed, err := hook.UninstallAll(root)
					if err != nil {
						wizard.Warn("hooks: " + err.Error())
					} else if len(removed) > 0 {
						wizard.Done("removed (or restored backup of) hooks: " + strings.Join(removed, ", "))
					} else {
						wizard.Skip("no hooks managed by mycelium — left untouched")
					}
				}
			}
		} else {
			wizard.Skip("no mycelium-managed git hooks found")
		}
	}

	// ── Step 3: MCP entry in ~/.claude.json ──────────────────────────
	wizard.Step("Claude Code MCP entry…")
	home, _ := os.UserHomeDir()
	if home != "" {
		configPath := filepath.Join(home, ".claude.json")
		if _, err := os.Stat(configPath); err == nil {
			if wizard.YN(fmt.Sprintf("  Remove mycelium entry from %s?", configPath), true, yes) {
				if dryRun {
					wizard.Skip("(dry-run) would delete mcpServers.mycelium")
				} else {
					removed, err := wizard.RemoveClaudeCodeMCP(configPath)
					if err != nil {
						wizard.Warn("MCP: " + err.Error())
					} else if removed {
						wizard.Done("removed mycelium from " + configPath)
					} else {
						wizard.Skip("no mycelium entry in " + configPath)
					}
				}
			}
		} else {
			wizard.Skip("~/.claude.json not found")
		}
	}

	// ── Step 4: .mycelium/ index directory ───────────────────────────
	if inRepo {
		wizard.Step("Project index directory…")
		mycoDir := filepath.Join(root, ".mycelium")
		if info, err := os.Stat(mycoDir); err == nil && info.IsDir() {
			if wizard.YN("  Delete .mycelium/ (index, telemetry, sessions)?", true, yes) {
				if dryRun {
					wizard.Skip("(dry-run) would rm -rf .mycelium/")
				} else if err := os.RemoveAll(mycoDir); err != nil {
					wizard.Warn("could not remove .mycelium/: " + err.Error())
				} else {
					wizard.Done("removed .mycelium/")
				}
			}
		} else {
			wizard.Skip("no .mycelium/ directory")
		}
	}

	// ── Step 5: .mycelium.yml config (only on --purge) ───────────────
	if inRepo && purge {
		wizard.Step("Project config (.mycelium.yml)…")
		cfgPath := filepath.Join(root, ".mycelium.yml")
		if _, err := os.Stat(cfgPath); err == nil {
			if wizard.YN("  Delete .mycelium.yml? (--purge)", true, yes) {
				if dryRun {
					wizard.Skip("(dry-run) would rm .mycelium.yml")
				} else if err := os.Remove(cfgPath); err != nil {
					wizard.Warn("could not remove .mycelium.yml: " + err.Error())
				} else {
					wizard.Done("removed .mycelium.yml")
				}
			}
		} else {
			wizard.Skip(".mycelium.yml not found")
		}
	}

	// ── Step 6: binaries on PATH (run last so we stay executable) ────
	if !keepBinary {
		wizard.Step("Binaries on PATH…")
		bins := findMycoBinaries(runningBinary)
		if len(bins) == 0 {
			wizard.Skip("no myco binaries found")
		}
		for _, p := range bins {
			label := p
			if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
				if dst, err := os.Readlink(p); err == nil {
					label = fmt.Sprintf("%s → %s", p, dst)
				}
			}
			if !wizard.YN(fmt.Sprintf("  Remove %s?", label), true, yes) {
				continue
			}
			if dryRun {
				wizard.Skip("(dry-run) would remove " + label)
				continue
			}
			if err := os.Remove(p); err != nil {
				if os.IsPermission(err) {
					fmt.Printf("    needs sudo: sudo rm %q\n", p)
				} else {
					wizard.Warn("could not remove " + p + ": " + err.Error())
				}
				continue
			}
			wizard.Done("removed " + p)
		}
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 54))
	if dryRun {
		fmt.Println("Dry-run complete. Re-run without --dry-run to apply.")
	} else {
		fmt.Println("Uninstall complete.")
		if !keepBinary {
			fmt.Println("Restart Claude Code so it stops trying to spawn the removed binary.")
		}
	}
	return nil
}

// findMycoBinaries returns every `myco` executable found on PATH plus the
// currently-running binary, deduplicated by path. Symlinks and their targets
// are both included so the user can decide on each.
func findMycoBinaries(runningPath string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			if dst, err := os.Readlink(p); err == nil {
				if !filepath.IsAbs(dst) {
					dst = filepath.Join(filepath.Dir(p), dst)
				}
				if _, err := os.Lstat(dst); err == nil && !seen[dst] {
					seen[dst] = true
					out = append(out, dst)
				}
			}
		}
	}
	if runningPath != "" {
		if _, err := os.Lstat(runningPath); err == nil {
			add(runningPath)
		}
	}
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "myco")
		if _, err := os.Lstat(candidate); err == nil {
			add(candidate)
		}
	}
	return out
}

// uninstallSessionHooks strips myco-installed entries from the project's
// .claude/settings.local.json hooks map. Returns true if any change was made.
func uninstallSessionHooks(repoRoot string) (bool, error) {
	settingsPath := filepath.Join(repoRoot, ".claude", "settings.local.json")
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	raw := map[string]any{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return false, fmt.Errorf("parse settings: %w", err)
	}
	hooks, _ := raw["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	changed := false
	for event, val := range hooks {
		list, ok := val.([]any)
		if !ok {
			continue
		}
		var keep []any
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				keep = append(keep, item)
				continue
			}
			hs, ok := m["hooks"].([]any)
			if !ok {
				keep = append(keep, item)
				continue
			}
			var keepInner []any
			for _, h := range hs {
				hm, ok := h.(map[string]any)
				if !ok {
					keepInner = append(keepInner, h)
					continue
				}
				cmd, _ := hm["command"].(string)
				if strings.Contains(cmd, "myco session ") {
					changed = true
					continue
				}
				keepInner = append(keepInner, h)
			}
			if len(keepInner) == 0 {
				changed = true
				continue
			}
			m["hooks"] = keepInner
			keep = append(keep, m)
		}
		if len(keep) == 0 {
			delete(hooks, event)
			changed = true
		} else {
			hooks[event] = keep
		}
	}
	if !changed {
		return false, nil
	}
	if len(hooks) == 0 {
		delete(raw, "hooks")
	} else {
		raw["hooks"] = hooks
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(settingsPath, append(out, '\n'), 0o644)
}

func errNotImplemented(name string) error {
	return fmt.Errorf("%s: not yet implemented (pre-v0.1 scaffolding)", name)
}
