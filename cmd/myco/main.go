package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jdwiederstein/mycelium/internal/config"
	"github.com/jdwiederstein/mycelium/internal/daemon"
	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/hook"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/ipc"
	"github.com/jdwiederstein/mycelium/internal/mcp"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/parser/python"
	"github.com/jdwiederstein/mycelium/internal/parser/typescript"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
	"github.com/jdwiederstein/mycelium/internal/watch"
)

var version = "0.1.0-dev"

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
		newDaemonCmd(),
		newMCPCmd(),
		newIndexCmd(),
		newQueryCmd(),
		newHookCmd(),
		newStatsCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// loadRepoCtx resolves the repo root and loads config (with defaults when
// .mycelium.yml is absent), so every command has a consistent starting point.
type repoCtx struct {
	Root string
	Cfg  config.Config
}

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

func newIndexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "index",
		Short: "Run a one-shot full index of the repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
			if err != nil {
				return err
			}
			defer ix.Close()

			reg := parser.NewRegistry()
			for _, lang := range rc.Cfg.Languages {
				switch lang {
				case "go":
					reg.Register(golang.New())
				case "typescript":
					reg.Register(typescript.New())
				case "python":
					reg.Register(python.New())
				}
			}

			w := repo.NewWalker(rc.Root, rc.Cfg.Include, rc.Cfg.Exclude, rc.Cfg.Index.MaxFileSizeKB)
			emb, err := embed.New(rc.Cfg.Embedder)
			if err != nil {
				return err
			}
			p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: w, Embedder: emb}

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

func newQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query the index from the shell",
	}

	findCmd := &cobra.Command{
		Use:   "find <name>",
		Short: "Find symbols by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, _ := cmd.Flags().GetString("kind")
			limit, _ := cmd.Flags().GetInt("limit")
			return runQueryFind(args[0], kind, limit)
		},
	}
	findCmd.Flags().String("kind", "", "filter by kind: function | method | type | interface | var | const")
	findCmd.Flags().Int("limit", 20, "max results")

	refsCmd := &cobra.Command{
		Use:   "refs <symbol>",
		Short: "Show references to a symbol (qualified name preferred, short name accepted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			return runQueryRefs(args[0], limit)
		},
	}
	refsCmd.Flags().Int("limit", 100, "max results")

	filesCmd := &cobra.Command{
		Use:   "files [name-contains]",
		Short: "List indexed files, optionally filtered by substring and language",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lang, _ := cmd.Flags().GetString("language")
			limit, _ := cmd.Flags().GetInt("limit")
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return runQueryFiles(name, lang, limit)
		},
	}
	filesCmd.Flags().String("language", "", "filter by language (go, typescript, python)")
	filesCmd.Flags().Int("limit", 500, "max results")

	outlineCmd := &cobra.Command{
		Use:   "outline <path>",
		Short: "Print the hierarchical symbol outline of a single file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQueryOutline(args[0])
		},
	}

	cmd.AddCommand(
		findCmd,
		refsCmd,
		filesCmd,
		outlineCmd,
		func() *cobra.Command {
			c := &cobra.Command{
				Use:   "search <text>",
				Short: "Semantic search (requires embedder configured in .mycelium.yml)",
				Args:  cobra.ExactArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					k, _ := cmd.Flags().GetInt("k")
					kind, _ := cmd.Flags().GetString("kind")
					path, _ := cmd.Flags().GetString("path")
					return runQuerySearch(args[0], k, kind, path)
				},
			}
			c.Flags().Int("k", 10, "number of results")
			c.Flags().String("kind", "", "filter by kind")
			c.Flags().String("path", "", "filter by path substring")
			return c
		}(),
	)
	return cmd
}

// daemonClient returns (client, ok) if a daemon is reachable at the configured
// socket. Callers that can fall back to a direct DB read should use this to
// avoid double-opening SQLite when the daemon owns the file.
func daemonClient(rc repoCtx) (*ipc.Client, bool) {
	c := ipc.NewClient(rc.Root + "/" + rc.Cfg.Daemon.Socket)
	if c.IsReachable() {
		return c, true
	}
	return nil, false
}

func runQueryFind(name, kind string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.SymbolHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodFindSymbol, ipc.FindSymbolParams{Name: name, Kind: kind, Limit: limit}, &hits); err != nil {
			return err
		}
	} else {
		ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.FindSymbol(ctx, name, kind, limit)
		if err != nil {
			return err
		}
	}
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "no matches")
		return nil
	}
	for _, h := range hits {
		fmt.Printf("%s:%d  [%s]  %s\n", h.Path, h.StartLine, h.Kind, h.Qualified)
		if h.Signature != "" {
			fmt.Printf("    %s\n", truncate(h.Signature, 120))
		}
	}
	return nil
}

func runQueryRefs(target string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.ReferenceHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodGetReferences, ipc.GetReferencesParams{Target: target, Limit: limit}, &hits); err != nil {
			return err
		}
	} else {
		ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.GetReferences(ctx, target, limit)
		if err != nil {
			return err
		}
	}
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "no references")
		return nil
	}
	for _, h := range hits {
		tag := "resolved"
		if !h.Resolved {
			tag = "textual"
		}
		src := h.SrcSymbolName
		if src == "" {
			src = "<top-level>"
		}
		fmt.Printf("%s:%d  [%s/%s]  %s  ->  %s\n", h.SrcPath, h.SrcLine, h.Kind, tag, src, h.DstName)
	}
	return nil
}

func runQueryFiles(nameContains, language string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.FileHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodListFiles, ipc.ListFilesParams{Language: language, NameContains: nameContains, Limit: limit}, &hits); err != nil {
			return err
		}
	} else {
		ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.ListFiles(ctx, language, nameContains, limit)
		if err != nil {
			return err
		}
	}
	for _, h := range hits {
		fmt.Printf("%s  [%s]  %d symbols  %d bytes\n", h.Path, h.Language, h.SymbolCount, h.SizeBytes)
	}
	return nil
}

func runQuerySearch(q string, k int, kind, pathContains string) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.SemanticHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodSearchSemantic, ipc.SearchSemanticParams{Query: q, K: k, Kind: kind, PathContains: pathContains}, &hits); err != nil {
			return err
		}
	} else {
		// Offline fallback: open the DB read-side ourselves. This can't happen
		// if the daemon is running (SQLite WAL allows concurrent readers).
		ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
		if err != nil {
			return err
		}
		defer ix.Close()
		emb, err := embed.New(rc.Cfg.Embedder)
		if err != nil {
			return err
		}
		r := query.NewReader(ix.DB())
		s := &query.Searcher{Reader: r, Embedder: emb}
		hits, err = s.SearchSemantic(ctx, q, k, kind, pathContains)
		if err != nil {
			return err
		}
	}
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "no results (is the embedder configured and are chunks embedded yet?)")
		return nil
	}
	for _, h := range hits {
		fmt.Printf("%s:%d  score=%.3f  [%s]  %s\n", h.Path, h.StartLine, h.Score, h.Kind, h.Qualified)
		if h.Signature != "" {
			fmt.Printf("    %s\n", truncate(h.Signature, 120))
		}
	}
	return nil
}

func runQueryOutline(path string) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var items []query.FileOutlineItem
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodGetFileOutline, ipc.GetFileOutlineParams{Path: path}, &items); err != nil {
			return err
		}
	} else {
		ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		items, err = r.GetFileOutline(ctx, path)
		if err != nil {
			return err
		}
	}
	if len(items) == 0 {
		fmt.Fprintln(os.Stderr, "no symbols (is the path indexed?)")
		return nil
	}
	var printItem func(it query.FileOutlineItem, depth int)
	printItem = func(it query.FileOutlineItem, depth int) {
		prefix := ""
		for i := 0; i < depth; i++ {
			prefix += "  "
		}
		fmt.Printf("%s%s:%d  [%s]  %s\n", prefix, path, it.StartLine, it.Kind, it.Name)
		for _, c := range it.Children {
			printItem(c, depth+1)
		}
	}
	for _, it := range items {
		printItem(it, 0)
	}
	return nil
}

func newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Print index status: languages, symbol counts, freshness",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
			if err != nil {
				return err
			}
			defer ix.Close()

			var s query.Stats
			if c, ok := daemonClient(rc); ok {
				if err := c.Call(ipc.MethodStats, nil, &s); err != nil {
					return err
				}
			} else {
				r := query.NewReader(ix.DB())
				s, err = r.Stats(ctx)
				if err != nil {
					return err
				}
			}
			fmt.Printf("files=%d symbols=%d refs=%d resolved=%d last_scan=%s\n", s.Files, s.Symbols, s.Refs, s.Resolved, s.LastScan.Format("2006-01-02 15:04:05"))
			fmt.Println("by kind:")
			for k, n := range s.ByKind {
				fmt.Printf("  %s: %d\n", k, n)
			}
			fmt.Println("by language:")
			for l, n := range s.ByLang {
				fmt.Printf("  %s: %d\n", l, n)
			}
			return nil
		},
	}
}

func newInitCmd() *cobra.Command {
	var mcpClient string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize mycelium in the current repo (writes .mycelium.yml, installs hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(mcpClient)
		},
	}
	cmd.Flags().StringVar(&mcpClient, "mcp", "", "register the MCP server with a client: claude | cursor")
	return cmd
}

func runInit(mcpClient string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := repo.DiscoverRoot(cwd)
	if err != nil {
		return err
	}
	cfgPath := root + "/" + config.DefaultPath

	// 1. Write .mycelium.yml if absent.
	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Printf("  %s already exists, keeping it\n", config.DefaultPath)
	} else {
		if err := config.WriteDefault(cfgPath); err != nil {
			return err
		}
		fmt.Printf("  wrote %s\n", config.DefaultPath)
	}

	// 2. Ensure .mycelium/ is gitignored.
	if err := ensureGitignoreEntry(root+"/.gitignore", ".mycelium/"); err != nil {
		return err
	}

	// 3. Install post-commit hook.
	installed, err := hook.InstallPostCommit(root)
	if err != nil {
		return err
	}
	if installed {
		fmt.Println("  installed .git/hooks/post-commit")
	} else {
		fmt.Println("  skipped git hook (not a git repo)")
	}

	// 4. Optional MCP client registration.
	if mcpClient != "" {
		if err := registerMCPClient(mcpClient, root); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not register MCP client %q: %v\n", mcpClient, err)
		}
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Start the daemon:       myco daemon &")
	fmt.Println("  2. Index the repo:         myco index   # (daemon also does this on start)")
	fmt.Println("  3. Try a query:            myco query find <name>")
	if mcpClient == "" {
		fmt.Println("  4. Wire it into an agent:  myco init --mcp claude   (or --mcp cursor)")
	}
	return nil
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
		fmt.Printf("  added %q to .gitignore\n", entry)
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

// registerMCPClient is intentionally conservative: Claude Code and Cursor
// config locations vary by OS and are user-editable. v0.3 prints the snippet
// the user should paste. A proper patcher lands in v0.4 once we've verified
// the config shape stays stable.
func registerMCPClient(which, root string) error {
	binary, err := os.Executable()
	if err != nil {
		binary = "myco"
	}
	snippet := fmt.Sprintf(`{
  "mcpServers": {
    "mycelium": {
      "command": "%s",
      "args": ["mcp"],
      "cwd": "%s"
    }
  }
}`, binary, root)

	fmt.Printf("\n  paste this into your %s MCP config:\n\n%s\n", which, snippet)
	return nil
}

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived indexer + query server for this repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runDaemon(ctx)
		},
	}
}

func runDaemon(ctx context.Context) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
	if err != nil {
		return err
	}
	defer ix.Close()

	reg := parser.NewRegistry()
	for _, lang := range rc.Cfg.Languages {
		switch lang {
		case "go":
			reg.Register(golang.New())
		case "typescript":
			reg.Register(typescript.New())
		case "python":
			reg.Register(python.New())
		}
	}

	w := repo.NewWalker(rc.Root, rc.Cfg.Include, rc.Cfg.Exclude, rc.Cfg.Index.MaxFileSizeKB)
	emb, err := embed.New(rc.Cfg.Embedder)
	if err != nil {
		return err
	}
	// If the user switched embedder models, drop stale vectors so we don't
	// mix dimensions. Cheap no-op when the model matches.
	if err := ix.InvalidateEmbeddingsForModel(ctx, emb.Model()); err != nil {
		return err
	}
	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: w, Embedder: emb}

	// Catch-up scan before accepting connections so the index reflects any
	// changes that happened while the daemon was down.
	if rep, err := p.RunOnce(ctx); err != nil {
		return fmt.Errorf("catch-up scan: %w", err)
	} else {
		fmt.Fprintf(os.Stderr, "[daemon] catch-up: scanned=%d changed=%d duration=%s\n",
			rep.FilesScanned, rep.FilesChanged, rep.Duration)
	}

	wat, err := watch.New(rc.Root, rc.Cfg.Include, rc.Cfg.Exclude, rc.Cfg.Index.MaxFileSizeKB, rc.Cfg.Watcher.DebounceMS)
	if err != nil {
		return err
	}

	// Kick off the embed worker. With a Noop embedder this returns immediately.
	worker := &pipeline.EmbedWorker{
		Index:         ix,
		Embedder:      emb,
		BatchSize:     rc.Cfg.Embedder.BatchSize,
		RatePerMinute: rc.Cfg.Embedder.RateLimitChunksPerMinute,
	}
	go worker.Run(ctx)

	d := &daemon.Daemon{
		Pipeline: p,
		Reader:   query.NewReader(ix.DB()),
		Embedder: emb,
		Watcher:  wat,
		Socket:   rc.Root + "/" + rc.Cfg.Daemon.Socket,
	}
	return d.Run(ctx)
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the MCP protocol over stdio (spawned by Claude Code, Cursor, etc.)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			client := ipc.NewClient(rc.Root + "/" + rc.Cfg.Daemon.Socket)
			if !client.IsReachable() {
				return fmt.Errorf("daemon is not running at %s — start it with `myco daemon &`", rc.Root+"/"+rc.Cfg.Daemon.Socket)
			}
			srv := &mcp.Server{In: os.Stdin, Out: os.Stdout, Client: client}
			return srv.Run(ctx)
		},
	}
}

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Git hook integrations",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "post-commit",
		Short: "Reconcile the index after a commit (invoked by .git/hooks/post-commit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			return hook.RunPostCommit(ctx, rc.Root+"/"+rc.Cfg.Daemon.Socket)
		},
	})
	return cmd
}

func errNotImplemented(name string) error {
	return fmt.Errorf("%s: not yet implemented (pre-v0.1 scaffolding)", name)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
