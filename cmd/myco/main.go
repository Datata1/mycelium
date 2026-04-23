package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jdwiederstein/mycelium/internal/config"
	"github.com/jdwiederstein/mycelium/internal/daemon"
	"github.com/jdwiederstein/mycelium/internal/doctor"
	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/hook"
	mychttp "github.com/jdwiederstein/mycelium/internal/http"
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
	goresolver "github.com/jdwiederstein/mycelium/internal/resolver/golang"
	pyresolver "github.com/jdwiederstein/mycelium/internal/resolver/python"
	tsresolver "github.com/jdwiederstein/mycelium/internal/resolver/typescript"
	"github.com/jdwiederstein/mycelium/internal/watch"
)

// version is set at build time via -ldflags "-X main.version=v1.0.0".
// Falls back to "dev" when built without ldflags (go run, plain go build).
var version = "dev"

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
		newDoctorCmd(),
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
			ix, err := openIndex(rc)
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
			resolvers := loadResolvers(rc.Root, rc.Cfg.Languages)
			wss, projFor, err := buildWorkspaces(ctx, rc, ix)
			if err != nil {
				return err
			}
			p := &pipeline.Pipeline{
				Index: ix, Registry: reg, Walker: w, Embedder: emb,
				Resolvers: resolvers, Workspaces: wss, FileProjectFor: projFor,
			}

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
			project, _ := cmd.Flags().GetString("project")
			return runQueryFind(args[0], kind, project, limit)
		},
	}
	findCmd.Flags().String("kind", "", "filter by kind: function | method | type | interface | var | const")
	findCmd.Flags().Int("limit", 20, "max results")
	findCmd.Flags().String("project", "", "restrict to a workspace project (by name)")

	refsCmd := &cobra.Command{
		Use:   "refs <symbol>",
		Short: "Show references to a symbol (qualified name preferred, short name accepted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			project, _ := cmd.Flags().GetString("project")
			return runQueryRefs(args[0], project, limit)
		},
	}
	refsCmd.Flags().Int("limit", 100, "max results")
	refsCmd.Flags().String("project", "", "restrict to a workspace project (by name)")

	filesCmd := &cobra.Command{
		Use:   "files [name-contains]",
		Short: "List indexed files, optionally filtered by substring and language",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lang, _ := cmd.Flags().GetString("language")
			limit, _ := cmd.Flags().GetInt("limit")
			project, _ := cmd.Flags().GetString("project")
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return runQueryFiles(name, lang, project, limit)
		},
	}
	filesCmd.Flags().String("language", "", "filter by language (go, typescript, python)")
	filesCmd.Flags().Int("limit", 500, "max results")
	filesCmd.Flags().String("project", "", "restrict to a workspace project (by name)")

	outlineCmd := &cobra.Command{
		Use:   "outline <path>",
		Short: "Print the hierarchical symbol outline of a single file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQueryOutline(args[0])
		},
	}

	grepCmd := &cobra.Command{
		Use:   "grep <pattern>",
		Short: "Ripgrep-style regex search across indexed files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _ := cmd.Flags().GetInt("k")
			path, _ := cmd.Flags().GetString("path")
			project, _ := cmd.Flags().GetString("project")
			return runQueryLexical(args[0], path, project, k)
		},
	}
	grepCmd.Flags().Int("k", 50, "max results")
	grepCmd.Flags().String("path", "", "filter by path substring")
	grepCmd.Flags().String("project", "", "restrict to a workspace project (by name)")

	summaryCmd := &cobra.Command{
		Use:   "summary <path>",
		Short: "Structural summary of a single file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuerySummary(args[0])
		},
	}

	neighborCmd := &cobra.Command{
		Use:   "neighbors <symbol>",
		Short: "Local call graph around a symbol",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			depth, _ := cmd.Flags().GetInt("depth")
			dir, _ := cmd.Flags().GetString("direction")
			project, _ := cmd.Flags().GetString("project")
			return runQueryNeighborhood(args[0], project, depth, dir)
		},
	}
	neighborCmd.Flags().Int("depth", 2, "traversal depth (max 5)")
	neighborCmd.Flags().String("direction", "both", "out | in | both")
	neighborCmd.Flags().String("project", "", "restrict seed lookup to a workspace project (traversal remains global)")

	cmd.AddCommand(
		findCmd,
		refsCmd,
		filesCmd,
		outlineCmd,
		grepCmd,
		summaryCmd,
		neighborCmd,
		func() *cobra.Command {
			c := &cobra.Command{
				Use:   "search <text>",
				Short: "Semantic search (requires embedder configured in .mycelium.yml)",
				Args:  cobra.ExactArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					k, _ := cmd.Flags().GetInt("k")
					kind, _ := cmd.Flags().GetString("kind")
					path, _ := cmd.Flags().GetString("path")
					project, _ := cmd.Flags().GetString("project")
					return runQuerySearch(args[0], k, kind, path, project)
				},
			}
			c.Flags().Int("k", 10, "number of results")
			c.Flags().String("kind", "", "filter by kind")
			c.Flags().String("path", "", "filter by path substring")
			c.Flags().String("project", "", "restrict to a workspace project (by name)")
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

func runQueryFind(name, kind, project string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.SymbolHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodFindSymbol, ipc.FindSymbolParams{Name: name, Kind: kind, Project: project, Limit: limit}, &hits); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.FindSymbol(ctx, name, kind, project, limit)
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

func runQueryRefs(target, project string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.ReferenceHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodGetReferences, ipc.GetReferencesParams{Target: target, Project: project, Limit: limit}, &hits); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.GetReferences(ctx, target, project, limit)
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

func runQueryFiles(nameContains, language, project string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.FileHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodListFiles, ipc.ListFilesParams{Language: language, NameContains: nameContains, Project: project, Limit: limit}, &hits); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.ListFiles(ctx, language, nameContains, project, limit)
		if err != nil {
			return err
		}
	}
	for _, h := range hits {
		fmt.Printf("%s  [%s]  %d symbols  %d bytes\n", h.Path, h.Language, h.SymbolCount, h.SizeBytes)
	}
	return nil
}

func runQueryLexical(pattern, pathContains, project string, k int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.LexicalHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodSearchLexical, ipc.SearchLexicalParams{Pattern: pattern, PathContains: pathContains, Project: project, K: k}, &hits); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.SearchLexical(ctx, pattern, pathContains, project, k, rc.Root)
		if err != nil {
			return err
		}
	}
	for _, h := range hits {
		fmt.Printf("%s:%d  %s\n", h.Path, h.Line, truncate(h.Snippet, 160))
	}
	return nil
}

func runQuerySummary(path string) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var s query.FileSummary
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodGetFileSummary, ipc.GetFileSummaryParams{Path: path}, &s); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		s, err = r.GetFileSummary(ctx, path)
		if err != nil {
			return err
		}
	}
	fmt.Printf("%s  [%s]  LOC=%d  symbols=%d\n", s.Path, s.Language, s.LOC, s.SymbolCount)
	if len(s.ByKind) > 0 {
		fmt.Print("  by kind:")
		for k, n := range s.ByKind {
			fmt.Printf(" %s=%d", k, n)
		}
		fmt.Println()
	}
	if len(s.Exports) > 0 {
		fmt.Println("  exports:")
		for _, e := range s.Exports {
			fmt.Printf("    %s:%d  [%s]  %s\n", s.Path, e.StartLine, e.Kind, e.Qualified)
		}
	}
	if len(s.Imports) > 0 {
		fmt.Println("  imports:")
		for _, im := range s.Imports {
			fmt.Printf("    %s\n", im)
		}
	}
	return nil
}

func runQueryNeighborhood(target, project string, depth int, direction string) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var nb query.Neighborhood
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodGetNeighborhood, ipc.GetNeighborhoodParams{Target: target, Project: project, Depth: depth, Direction: direction}, &nb); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		nb, err = r.GetNeighborhood(ctx, target, project, depth, query.Direction(direction))
		if err != nil {
			return err
		}
	}
	for _, note := range nb.Notes {
		fmt.Fprintf(os.Stderr, "note: %s\n", note)
	}
	fmt.Printf("seed: %s  (%s:%d)\n", nb.Seed.Qualified, nb.Seed.Path, nb.Seed.StartLine)
	for _, e := range nb.Edges {
		arrow := "->"
		if e.Direction == "in" {
			arrow = "<-"
		}
		fmt.Printf("  d=%d  %s  %s  %s  (%s:%d)\n", e.Depth, e.FromName, arrow, e.ToName, e.SrcPath, e.SrcLine)
	}
	return nil
}

func runQuerySearch(q string, k int, kind, pathContains, project string) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.SemanticHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodSearchSemantic, ipc.SearchSemanticParams{Query: q, K: k, Kind: kind, PathContains: pathContains, Project: project}, &hits); err != nil {
			return err
		}
	} else {
		// Offline fallback: open the DB read-side ourselves. This can't happen
		// if the daemon is running (SQLite WAL allows concurrent readers).
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		emb, err := embed.New(rc.Cfg.Embedder)
		if err != nil {
			return err
		}
		r := query.NewReader(ix.DB())
		// Ensure vec0 is ready for the offline path too, so `myco query
		// search` without a running daemon still uses the fast path when
		// the extension is configured.
		if emb.Dimension() > 0 && rc.Cfg.Index.Vector.AutoCreate {
			_ = ix.EnsureVSS(ctx, emb.Dimension())
		}
		s := &query.Searcher{Reader: r, Embedder: emb, VSSTable: ix.VSSTableName()}
		hits, err = s.SearchSemantic(ctx, q, k, kind, pathContains, project)
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
		ix, err := openIndex(rc)
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
			ix, err := openIndex(rc)
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
			return nil
		},
	}
}

func newDoctorCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run health checks on the index; exit 0/1/2 on pass/warn/fail",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			// `doctor` is a read-only check, so a direct DB open is fine
			// whether or not the daemon is running (SQLite WAL allows
			// concurrent readers).
			ix, err := openIndex(rc)
			if err != nil {
				return err
			}
			defer ix.Close()

			r := query.NewReader(ix.DB())
			rep, err := doctor.Run(ctx, r, rc.Cfg.Embedder.Provider, doctor.ThresholdsFromConfig(rc.Cfg))
			if err != nil {
				return err
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
			if code := rep.ExitCode(); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit the full report as JSON (for CI)")
	return cmd
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
	ix, err := openIndex(rc)
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
	resolvers := loadResolvers(rc.Root, rc.Cfg.Languages)
	// v1.4: create the vec0 virtual table if sqlite-vec loaded. Safe no-op
	// when the extension isn't configured; mirrors chunks.embedding so
	// search_semantic can switch to the fast path transparently.
	if emb.Dimension() > 0 && rc.Cfg.Index.Vector.AutoCreate {
		if err := ix.EnsureVSS(ctx, emb.Dimension()); err != nil {
			fmt.Fprintf(os.Stderr, "[vss] ensure vss_chunks: %v\n", err)
		}
	}
	wss, projFor, err := buildWorkspaces(ctx, rc, ix)
	if err != nil {
		return err
	}
	p := &pipeline.Pipeline{
		Index: ix, Registry: reg, Walker: w, Embedder: emb,
		Resolvers: resolvers, Workspaces: wss, FileProjectFor: projFor,
	}

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
		RepoRoot: rc.Root,
		VSSTable: ix.VSSTableName(),
	}

	// Start the HTTP transport alongside the unix socket. Disabled when
	// config.daemon.http_port = 0.
	if rc.Cfg.Daemon.HTTPPort > 0 {
		httpSrv := &mychttp.Server{
			Port:       rc.Cfg.Daemon.HTTPPort,
			Dispatcher: d,
			Logger:     func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[http] "+f+"\n", a...) },
		}
		if err := httpSrv.Start(ctx); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[daemon] http api on 127.0.0.1:%d\n", rc.Cfg.Daemon.HTTPPort)
		defer httpSrv.Close()
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
			srv := &mcp.Server{In: os.Stdin, Out: os.Stdout, Client: client, Version: version}
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

// openIndex opens the repo's SQLite index and applies the vector-extension
// config. Callers don't need to care whether sqlite-vec loaded — the
// returned Index transparently handles both fast and fallback paths.
func openIndex(rc repoCtx) (*index.Index, error) {
	return index.OpenWithExtension(rc.Root+"/"+rc.Cfg.Index.Path, rc.Cfg.Index.Vector.ExtensionPath)
}

// buildWorkspaces materializes the v1.5 per-project walkers from config
// and upserts the matching projects-table rows. On a v1.4 config (no
// projects: list) it returns nil — the pipeline falls back to its
// legacy single-Walker path automatically. Projects not present in the
// current config are pruned from the DB (cascade removes their files).
func buildWorkspaces(ctx context.Context, rc repoCtx, ix *index.Index) ([]pipeline.Workspace, func(string) int64, error) {
	if len(rc.Cfg.Projects) == 0 {
		return nil, nil, nil
	}
	var (
		workspaces []pipeline.Workspace
		keep       []int64
		// prefixes feeds the watcher-path prefix resolver. We sort by
		// descending length so longest-prefix wins (so a project
		// nested inside another gets the nested id).
		prefixes []struct {
			abs string
			id  int64
		}
	)
	for _, pc := range rc.Cfg.Projects {
		id, err := ix.UpsertProject(ctx, pc.Name, pc.Root)
		if err != nil {
			return nil, nil, fmt.Errorf("project %s: %w", pc.Name, err)
		}
		keep = append(keep, id)
		absRoot := rc.Root + "/" + pc.Root
		include := pc.Include
		if len(include) == 0 {
			include = rc.Cfg.Include
		}
		exclude := pc.Exclude
		if len(exclude) == 0 {
			exclude = rc.Cfg.Exclude
		}
		w := repo.NewWalker(absRoot, include, exclude, rc.Cfg.Index.MaxFileSizeKB)
		workspaces = append(workspaces, pipeline.Workspace{ProjectID: id, Walker: w})
		prefixes = append(prefixes, struct {
			abs string
			id  int64
		}{abs: absRoot, id: id})
	}
	if err := ix.PruneProjects(ctx, keep); err != nil {
		return nil, nil, fmt.Errorf("prune projects: %w", err)
	}
	// Longest-prefix wins. Sorting once is cheap vs. sorting per event.
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i].abs) > len(prefixes[j].abs) })
	resolver := func(abs string) int64 {
		for _, p := range prefixes {
			if len(abs) >= len(p.abs) && abs[:len(p.abs)] == p.abs &&
				(len(abs) == len(p.abs) || abs[len(p.abs)] == '/') {
				return p.id
			}
		}
		return 0
	}
	return workspaces, resolver, nil
}

// loadResolvers constructs one resolver per enabled language. Returns a map
// the pipeline indexes into; languages without a resolver simply don't get
// type-aware rewrites (textual-only fallback stays in place). Go needs an
// up-front go/packages load; TS + Python are stateless and always ready.
func loadResolvers(repoRoot string, languages []string) map[string]pipeline.Resolver {
	out := map[string]pipeline.Resolver{}
	for _, l := range languages {
		switch l {
		case "go":
			r := goresolver.New(repoRoot)
			errCount, err := r.Load()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[resolver] go-types unavailable: %v — falling back to textual resolution\n", err)
				continue
			}
			if errCount > 0 {
				fmt.Fprintf(os.Stderr, "[resolver] go-types loaded with %d package errors (inspect via `myco doctor`)\n", errCount)
			}
			out["go"] = r
		case "typescript":
			out["typescript"] = tsresolver.New()
		case "python":
			out["python"] = pyresolver.New()
		}
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
