package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/config"
	"github.com/datata1/mycelium/internal/doctor"
	"github.com/datata1/mycelium/internal/hook"
	"github.com/datata1/mycelium/internal/repo"
	"github.com/datata1/mycelium/internal/service"
	"github.com/datata1/mycelium/internal/wizard"
)

func newInitCmd() *cobra.Command {
	var (
		acceptAll   bool
		doctorAfter bool
		gitExclude  bool
		userMode    bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive setup wizard: config, MCP, CLAUDE.md, git hook (v3.2)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if userMode {
				return runUserInit(acceptAll)
			}
			return runWizard(acceptAll, doctorAfter, gitExclude)
		},
	}
	cmd.Flags().BoolVarP(&acceptAll, "yes", "y", false,
		"accept all defaults without prompting (CI / non-interactive)")
	cmd.Flags().BoolVar(&doctorAfter, "doctor-after", false,
		"run myco doctor at the end and exit non-zero on warn/fail (CI gate)")
	cmd.Flags().BoolVar(&gitExclude, "git-exclude", false,
		"write ignore rules to .git/info/exclude instead of .gitignore (per-checkout, never committed)")
	cmd.Flags().BoolVar(&userMode, "user", false,
		"create/update the user-level config at ~/.config/myco/config.yml")
	return cmd
}

func runWizard(yes, doctorAfter, gitExclude bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := repo.DiscoverRoot(cwd)
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		binary = "myco"
	}

	fmt.Printf("Mycelium setup  ·  %s\n", root)
	fmt.Println(strings.Repeat("─", 54))

	// ── Step 1: language detection ────────────────────────────────────
	wizard.Step("Detecting languages…")
	langs, err := wizard.DetectLanguages(root)
	if err != nil {
		wizard.Warn("language scan failed: " + err.Error())
	}
	var chosenLangs []string
	if len(langs) == 0 {
		wizard.Warn("no Go / TypeScript / Python files found — defaulting to all three")
		chosenLangs = []string{"go", "typescript", "python"}
	} else {
		var detected []string
		for _, l := range langs {
			detected = append(detected, fmt.Sprintf("%s (%d files)", l.Language, l.Count))
		}
		fmt.Printf("  Found: %s\n", strings.Join(detected, ", "))
		for _, l := range langs {
			chosenLangs = append(chosenLangs, l.Language)
		}
	}

	// ── Step 2: monorepo detection ────────────────────────────────────
	wizard.Step("Scanning for workspace sub-projects…")
	subs, err := wizard.DetectSubprojects(root)
	if err != nil {
		wizard.Warn("subproject scan failed: " + err.Error())
	}
	var projects []config.ProjectConfig
	if len(subs) > 0 {
		fmt.Printf("  Monorepo? Found %d sub-projects:\n", len(subs))
		for _, s := range subs {
			fmt.Printf("    %s  (%s)\n", s.RelDir, s.MarkerFile)
		}
		if wizard.YN("  Set up as workspace projects?", true, yes) {
			for _, s := range subs {
				name := wizard.Str(fmt.Sprintf("    Name for %s", s.RelDir), s.SuggestedName, yes)
				projects = append(projects, config.ProjectConfig{Name: name, Root: s.RelDir})
			}
		}
	} else {
		wizard.Skip("single-project repo (no sub go.mod / package.json found)")
	}

	// ── Step 3: write .mycelium.yml ──────────────────────────────────
	wizard.Step("Writing config…")
	cfgPath := root + "/" + config.DefaultPath
	if _, err := os.Stat(cfgPath); err == nil {
		wizard.Skip(config.DefaultPath + " already exists — keeping it")
	} else {
		enableTelemetry := wizard.YN(
			"  Enable telemetry? (records tool call stats; powers `myco doctor` adoption check)",
			true, yes,
		)
		watcherBackend := ""
		if wmPath, ok := wizard.WatchmanAvailable(); ok {
			fmt.Printf("  watchman found at %s\n", wmPath)
			opts := []string{
				"fsnotify  (built-in, zero dependencies)",
				"watchman  (opt-in, lower latency on large repos)",
			}
			if wizard.Choice("  File watcher backend:", opts, yes) == 1 {
				watcherBackend = "watchman"
			}
		} else {
			wizard.Skip("file watcher: fsnotify (watchman not installed)")
		}
		cfg := buildConfig(chosenLangs, projects, enableTelemetry, watcherBackend)
		if err := config.Write(cfgPath, cfg); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		wizard.Done("wrote " + config.DefaultPath)
	}

	// ── Step 4: .gitignore ───────────────────────────────────────────
	handleGitignore(root, yes, gitExclude)

	// ── Step 5: git hooks ────────────────────────────────────────────
	installed, err := hook.InstallAll(root)
	if err != nil {
		wizard.Warn("git hook install failed: " + err.Error())
	} else if len(installed) > 0 {
		wizard.Done("installed git hooks: " + strings.Join(installed, ", "))
		if custom := gitHooksPathOverride(root); custom != "" {
			wizard.Warn(fmt.Sprintf(
				"git config core.hooksPath=%s — git will ignore .git/hooks, so these hooks won't fire; the daemon's catch-up scan still reconciles on restart",
				custom))
		}
	} else {
		wizard.Skip("git hooks (not a git repo)")
	}

	// ── Step 6: MCP client registration ──────────────────────────────
	wizard.Step("MCP client registration…")
	clients := wizard.DetectMCPClients()
	var mcpOptions []string
	for _, c := range clients {
		label := c.Name
		if c.Detected {
			label += " — config found ✓"
		} else {
			label += " — not detected"
		}
		mcpOptions = append(mcpOptions, label)
	}
	mcpOptions = append(mcpOptions, "Print snippet only", "Skip")

	choice := wizard.Choice("  Register with MCP client:", mcpOptions, yes)
	switch {
	case choice < len(clients):
		c := clients[choice]
		if wizard.YN(fmt.Sprintf("  Write mycelium into %s?", c.ConfigPath), true, yes) {
			if err := wizard.WriteClaudeCodeMCP(c.ConfigPath, binary, root); err != nil {
				wizard.Warn("could not write MCP config: " + err.Error())
				fmt.Println()
				fmt.Println("  Paste this instead:")
				fmt.Println(wizard.MCPSnippet(binary, root))
			} else {
				wizard.Done("registered in " + c.ConfigPath)
				fmt.Println("  Restart your agent client for MCP to take effect.")
				fmt.Println()
				fmt.Println("  Session hooks record which myco tools vs. grep/Read")
				fmt.Println("  the agent uses per conversation (UserPromptSubmit /")
				fmt.Println("  PostToolUse / Stop).")
				if wizard.YN("  Install session tracking hooks?", true, yes) {
					// The blocking verify gate stays opt-in via
					// `myco session hooks install --verify-gate`.
					if err := installSessionHooks(root, binary, false); err != nil {
						wizard.Warn("hooks install failed: " + err.Error())
					} else {
						wizard.Done("session hooks written to .claude/settings.json")
					}
				}
			}
		}
	case choice == len(clients): // print snippet
		fmt.Println()
		fmt.Println("  Paste this into your agent's MCP config:")
		fmt.Println(wizard.MCPSnippet(binary, root))
	default: // skip
		wizard.Skip("MCP registration")
	}

	// ── Step 7: CLAUDE.md priming snippet ────────────────────────────
	claudeMDPath := root + "/CLAUDE.md"
	if _, err := os.Stat(claudeMDPath); err == nil {
		wizard.Step("CLAUDE.md priming…")
		fmt.Println("  A short block helps the agent know when to reach for myco tools.")
		if wizard.YN("  Append to CLAUDE.md?", true, yes) {
			wrote, err := wizard.AppendPrimingSnippet(claudeMDPath)
			if err != nil {
				wizard.Warn("could not write CLAUDE.md: " + err.Error())
			} else if wrote {
				wizard.Done("appended priming snippet to CLAUDE.md")
			} else {
				wizard.Skip("snippet already present in CLAUDE.md")
			}
		} else {
			fmt.Println()
			fmt.Println("  Snippet (copy manually):")
			fmt.Println(wizard.PrimingSnippet())
		}
	}

	// ── Next steps ────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println(strings.Repeat("─", 54))
	fmt.Println("Next steps:")
	fmt.Println("  task daemon    # start the daemon (blocks; run in a separate terminal)")
	fmt.Println("  task check     # vet + test before pushing")
	fmt.Println("  myco doctor    # health check once the daemon has indexed")

	// ── Git hygiene tip ───────────────────────────────────────────────
	printGitHygieneTip(root)

	// ── --doctor-after (CI gate) ─────────────────────────────────────
	if doctorAfter {
		fmt.Println()
		ix, err := openIndex(repoCtx{Root: root, Cfg: config.Default()})
		if err != nil {
			return fmt.Errorf("doctor: open index: %w", err)
		}
		defer ix.Close()
		ctx := context.Background()
		r := service.NewReadOnly(ix, root, nil).Reader()
		stateDir := repoCtx{Root: root, Cfg: config.Default()}.AbsStateDir()
		rep, err := doctor.Run(ctx, r, doctor.ThresholdsFromConfig(config.Default()), root, stateDir)
		if err != nil {
			return fmt.Errorf("doctor: %w", err)
		}
		printDoctorReport(rep)
		if code := rep.ExitCode(); code != 0 {
			os.Exit(code)
		}
	}
	return nil
}

// buildConfig constructs a Config populated from wizard choices.
func buildConfig(langs []string, projects []config.ProjectConfig, tel bool, watcherBackend string) config.Config {
	cfg := config.Default()
	cfg.Languages = langs
	cfg.Projects = projects
	cfg.Telemetry.Enabled = tel
	cfg.Watcher.Backend = watcherBackend
	return cfg
}

// handleGitignore decides where to write the .mycelium/ ignore rules:
// .gitignore (default) or .git/info/exclude (when --git-exclude is set or
// .gitignore is org-managed / tracked).
func handleGitignore(root string, yes, forceExclude bool) {
	if forceExclude {
		writeToGitExclude(root)
		return
	}
	// Detect whether .gitignore is tracked by the repo. If it is, the
	// org owns it and we shouldn't touch it.
	if isGitTracked(root, ".gitignore") {
		wizard.Warn(".gitignore is tracked by git — won't modify a committed file")
		if wizard.YN("  Write ignore rules to .git/info/exclude instead?", true, yes) {
			writeToGitExclude(root)
		} else {
			wizard.Skip("gitignore update skipped")
			fmt.Println("  To hide .mycelium/ manually, use one of:")
			fmt.Println("    echo '.mycelium/' >> .git/info/exclude  (this checkout)")
			fmt.Println("    echo '.mycelium/' >> ~/.config/git/ignore  (all repos)")
		}
		return
	}
	// Standard path: append to .gitignore.
	for _, entry := range []string{".mycelium/", ".mycelium.yml"} {
		if err := appendIgnoreEntry(root+"/.gitignore", entry); err != nil {
			wizard.Warn("could not update .gitignore: " + err.Error())
			return
		}
	}
	wizard.Done("added .mycelium/ and .mycelium.yml to .gitignore")
}

func writeToGitExclude(root string) {
	excludePath := filepath.Join(root, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		wizard.Warn("could not create .git/info/: " + err.Error())
		return
	}
	for _, entry := range []string{".mycelium/", ".mycelium.yml"} {
		if err := appendIgnoreEntry(excludePath, entry); err != nil {
			wizard.Warn("could not update .git/info/exclude: " + err.Error())
			return
		}
	}
	wizard.Done("added .mycelium/ and .mycelium.yml to .git/info/exclude (per-checkout, not committed)")
}

// appendIgnoreEntry adds entry to path if not already present, creating the
// file when absent.
func appendIgnoreEntry(path, entry string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range splitLines(string(existing)) {
		if line == entry {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		_, _ = f.WriteString("\n")
	}
	_, err = f.WriteString(entry + "\n")
	return err
}

// isGitTracked reports whether relPath is tracked in the git index at root.
func isGitTracked(root, relPath string) bool {
	cmd := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", relPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// printGitHygieneTip checks whether any mycelium files are still untracked
// after init and prints a concise tip if so.
func printGitHygieneTip(root string) {
	cmd := exec.Command("git", "-C", root, "status", "--porcelain", "--", ".mycelium/", ".mycelium.yml")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return
	}
	hasUntracked := false
	for _, line := range splitLines(string(out)) {
		if strings.HasPrefix(line, "??") {
			hasUntracked = true
			break
		}
	}
	if !hasUntracked {
		return
	}
	fmt.Println()
	fmt.Println("Git hygiene tip: mycelium files are still untracked.")
	fmt.Println("  Options:")
	fmt.Println("    myco init --git-exclude          # write to .git/info/exclude (this checkout)")
	fmt.Println("    echo '.mycelium/' >> ~/.config/git/ignore   # hide in all repos globally")
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// runUserInit creates or updates ~/.config/myco/config.yml with user-level
// defaults. Unlike runWizard it is repo-agnostic: it never writes .mycelium.yml,
// .gitignore, or git hooks. Those remain repo-level concerns.
func runUserInit(yes bool) error {
	userPath, err := config.UserConfigPath()
	if err != nil {
		return fmt.Errorf("locate user config: %w", err)
	}

	fmt.Printf("Mycelium user config  ·  %s\n", userPath)
	fmt.Println(strings.Repeat("─", 54))

	// Load existing user config so re-running is idempotent.
	var u config.UserConfig
	if _, serr := os.Stat(userPath); serr == nil {
		if loaded, lerr := config.LoadUser(userPath); lerr == nil {
			u = loaded
		}
	}
	u.Version = config.CurrentVersion

	// ── Language defaults ────────────────────────────────────────────
	wizard.Step("Language defaults…")
	if len(u.Languages) == 0 {
		u.Languages = []string{"go", "typescript", "python"}
	}
	fmt.Printf("  Current: %s\n", strings.Join(u.Languages, ", "))
	if !wizard.YN("  Keep these language defaults?", true, yes) {
		// Let the user type a comma-separated list.
		raw := wizard.Str("  Languages (comma-separated)", strings.Join(u.Languages, ", "), yes)
		var langs []string
		for _, l := range strings.Split(raw, ",") {
			l = strings.TrimSpace(l)
			if l != "" {
				langs = append(langs, l)
			}
		}
		if len(langs) > 0 {
			u.Languages = langs
		}
	}

	// ── Telemetry ────────────────────────────────────────────────────
	wizard.Step("Telemetry…")
	u.Telemetry.Enabled = wizard.YN(
		"  Enable telemetry globally? (local-only; powers `myco doctor` adoption check)",
		u.Telemetry.Enabled, yes,
	)

	// ── External cache path ──────────────────────────────────────────
	// Only offered when we can identify a repo root to hash.
	cwd, _ := os.Getwd()
	root, rerr := repo.DiscoverRoot(cwd)
	if rerr == nil {
		wizard.Step("Index location…")
		hash := repoHash(root)
		cacheDir, cerr := repoStateDirForHash(hash)
		if cerr == nil {
			fmt.Printf("  Repo root: %s  (id: %s)\n", root, hash)
			fmt.Printf("  External cache: %s\n", cacheDir)
			fmt.Println("  Storing index outside the repo prevents .mycelium/ appearing in git status.")
			if wizard.YN("  Store index files in external cache?", true, yes) {
				u.Index.Path = tildeHome(filepath.Join(cacheDir, "index.db"))
				u.Daemon.Socket = tildeHome(filepath.Join(cacheDir, "daemon.sock"))
				wizard.Done(fmt.Sprintf("index → %s", u.Index.Path))
			}
		}
	} else {
		wizard.Skip("index location (not in a git repo)")
	}

	// ── Write ────────────────────────────────────────────────────────
	if err := config.WriteUser(userPath, u); err != nil {
		return fmt.Errorf("write user config: %w", err)
	}
	wizard.Done("wrote " + userPath)
	fmt.Println()
	fmt.Println("Tip: run `myco init --user` again from any repo to configure its cache path.")
	fmt.Println("     Per-repo .mycelium.yml always takes priority over this file.")
	return nil
}

// repoHash returns the first 12 hex chars of SHA-256(absRoot), used as a
// stable, short directory name for the XDG cache path.
func repoHash(absRoot string) string {
	sum := sha256.Sum256([]byte(absRoot))
	const hexChars = "0123456789abcdef"
	var b [12]byte
	for i := 0; i < 6; i++ {
		b[i*2] = hexChars[sum[i]>>4]
		b[i*2+1] = hexChars[sum[i]&0xf]
	}
	return string(b[:])
}

// repoStateDirForHash returns the XDG cache directory for the given repo hash.
func repoStateDirForHash(hash string) (string, error) {
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheBase, "myco", hash), nil
}

// tildeHome replaces the user's home directory prefix with "~/" so the path
// stored in config is portable across home directory renames.
func tildeHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if strings.HasPrefix(p, home+"/") {
		return "~/" + p[len(home)+1:]
	}
	return p
}

// gitHooksPathOverride returns the value of git config core.hooksPath when
// set (husky and friends redirect hooks there, making .git/hooks inert), or
// "" when unset or git is unavailable.
func gitHooksPathOverride(root string) string {
	cmd := exec.Command("git", "config", "core.hooksPath")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
