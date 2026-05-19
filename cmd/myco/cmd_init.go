package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jdwiederstein/mycelium/internal/config"
	"github.com/jdwiederstein/mycelium/internal/doctor"
	"github.com/jdwiederstein/mycelium/internal/hook"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
	"github.com/jdwiederstein/mycelium/internal/wizard"
)

func newInitCmd() *cobra.Command {
	var (
		acceptAll   bool
		doctorAfter bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive setup wizard: config, MCP, CLAUDE.md, git hook (v3.2)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWizard(acceptAll, doctorAfter)
		},
	}
	cmd.Flags().BoolVarP(&acceptAll, "yes", "y", false,
		"accept all defaults without prompting (CI / non-interactive)")
	cmd.Flags().BoolVar(&doctorAfter, "doctor-after", false,
		"run myco doctor at the end and exit non-zero on warn/fail (CI gate)")
	return cmd
}

func runWizard(yes, doctorAfter bool) error {
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
	if err := ensureGitignoreEntry(root+"/.gitignore", ".mycelium/"); err != nil {
		wizard.Warn("could not update .gitignore: " + err.Error())
	}

	// ── Step 5: git hook ─────────────────────────────────────────────
	installed, err := hook.InstallPostCommit(root)
	if err != nil {
		wizard.Warn("git hook install failed: " + err.Error())
	} else if installed {
		wizard.Done("installed .git/hooks/post-commit")
	} else {
		wizard.Skip("git hook (not a git repo)")
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
					if err := installSessionHooks(root, binary); err != nil {
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

	// ── --doctor-after (CI gate) ─────────────────────────────────────
	if doctorAfter {
		fmt.Println()
		ix, err := openIndex(repoCtx{Root: root, Cfg: config.Default()})
		if err != nil {
			return fmt.Errorf("doctor: open index: %w", err)
		}
		defer ix.Close()
		ctx := context.Background()
		r := query.NewReader(ix.DB())
		rep, err := doctor.Run(ctx, r, doctor.ThresholdsFromConfig(config.Default()), root)
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

func ensureGitignoreEntry(path, entry string) error {
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
	if err == nil {
		wizard.Done("added .mycelium/ to .gitignore")
	}
	return err
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
