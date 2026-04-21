package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jdwiederstein/mycelium/internal/config"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
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
				// typescript, python parsers land in v0.3
				}
			}

			w := repo.NewWalker(rc.Root, rc.Cfg.Include, rc.Cfg.Exclude, rc.Cfg.Index.MaxFileSizeKB)
			p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: w}

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
		&cobra.Command{
			Use:   "search <text>",
			Short: "Semantic search (requires embedder)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return errNotImplemented("query search")
			},
		},
	)
	return cmd
}

func runQueryFind(name, kind string, limit int) error {
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

	r := query.NewReader(ix.DB())
	hits, err := r.FindSymbol(ctx, name, kind, limit)
	if err != nil {
		return err
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
	ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
	if err != nil {
		return err
	}
	defer ix.Close()

	r := query.NewReader(ix.DB())
	hits, err := r.GetReferences(ctx, target, limit)
	if err != nil {
		return err
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
	ix, err := index.Open(rc.Root + "/" + rc.Cfg.Index.Path)
	if err != nil {
		return err
	}
	defer ix.Close()

	r := query.NewReader(ix.DB())
	hits, err := r.ListFiles(ctx, language, nameContains, limit)
	if err != nil {
		return err
	}
	for _, h := range hits {
		fmt.Printf("%s  [%s]  %d symbols  %d bytes\n", h.Path, h.Language, h.SymbolCount, h.SizeBytes)
	}
	return nil
}

func runQueryOutline(path string) error {
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

	r := query.NewReader(ix.DB())
	items, err := r.GetFileOutline(ctx, path)
	if err != nil {
		return err
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

			r := query.NewReader(ix.DB())
			s, err := r.Stats(ctx)
			if err != nil {
				return err
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
			return errNotImplemented("init")
		},
	}
	cmd.Flags().StringVar(&mcpClient, "mcp", "", "register the MCP server with a client: claude | cursor")
	return cmd
}

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived indexer + query server for this repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("daemon")
		},
	}
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the MCP protocol over stdio (spawned by Claude Code, Cursor, etc.)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("mcp")
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
			return errNotImplemented("hook post-commit")
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
