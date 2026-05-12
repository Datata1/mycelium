package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/jdwiederstein/mycelium/internal/config"
	"github.com/jdwiederstein/mycelium/internal/daemon"
	"github.com/jdwiederstein/mycelium/internal/wizard"
	"github.com/jdwiederstein/mycelium/internal/doctor"
	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/gitref"
	"github.com/jdwiederstein/mycelium/internal/hook"
	mychttp "github.com/jdwiederstein/mycelium/internal/http"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/ipc"
	"github.com/jdwiederstein/mycelium/internal/mcp"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/document"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/parser/python"
	"github.com/jdwiederstein/mycelium/internal/parser/typescript"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
	"github.com/jdwiederstein/mycelium/internal/skills"
	goresolver "github.com/jdwiederstein/mycelium/internal/resolver/golang"
	pyresolver "github.com/jdwiederstein/mycelium/internal/resolver/python"
	tsresolver "github.com/jdwiederstein/mycelium/internal/resolver/typescript"
	"github.com/jdwiederstein/mycelium/internal/telemetry"
	"github.com/jdwiederstein/mycelium/internal/watch"
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
		newSkillsCmd(),
		newReadCmd(),
		newSessionCmd(),
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
				Documents: buildDocumentRegistry(),
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
			since, _ := cmd.Flags().GetString("since")
			focus, _ := cmd.Flags().GetString("focus")
			return runQueryFind(args[0], kind, project, since, focus, limit)
		},
	}
	findCmd.Flags().String("kind", "", "filter by kind: function | method | type | interface | var | const")
	findCmd.Flags().Int("limit", 20, "max results")
	findCmd.Flags().String("project", "", "restrict to a workspace project (by name)")
	findCmd.Flags().String("since", "", "restrict to files changed between <ref>...HEAD")
	findCmd.Flags().String("focus", "", "v2.4 lexical focus filter — keep + rerank hits matching this hint")

	refsCmd := &cobra.Command{
		Use:   "refs <symbol>",
		Short: "Show references to a symbol (qualified name preferred, short name accepted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			project, _ := cmd.Flags().GetString("project")
			since, _ := cmd.Flags().GetString("since")
			return runQueryRefs(args[0], project, since, limit)
		},
	}
	refsCmd.Flags().Int("limit", 100, "max results")
	refsCmd.Flags().String("project", "", "restrict to a workspace project (by name)")
	refsCmd.Flags().String("since", "", "restrict to files changed between <ref>...HEAD")

	filesCmd := &cobra.Command{
		Use:   "files [name-contains]",
		Short: "List indexed files, optionally filtered by substring and language",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lang, _ := cmd.Flags().GetString("language")
			limit, _ := cmd.Flags().GetInt("limit")
			project, _ := cmd.Flags().GetString("project")
			since, _ := cmd.Flags().GetString("since")
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return runQueryFiles(name, lang, project, since, limit)
		},
	}
	filesCmd.Flags().String("language", "", "filter by language (go, typescript, python)")
	filesCmd.Flags().Int("limit", 500, "max results")
	filesCmd.Flags().String("project", "", "restrict to a workspace project (by name)")
	filesCmd.Flags().String("since", "", "restrict to files changed between <ref>...HEAD")

	outlineCmd := &cobra.Command{
		Use:   "outline <path>",
		Short: "Print the hierarchical symbol outline of a single file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			focus, _ := cmd.Flags().GetString("focus")
			return runQueryOutline(args[0], focus)
		},
	}
	outlineCmd.Flags().String("focus", "", "v2.4 lexical focus filter — keep top-level items whose subtree matches")

	grepCmd := &cobra.Command{
		Use:   "grep <pattern>",
		Short: "Ripgrep-style regex search across indexed files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _ := cmd.Flags().GetInt("k")
			path, _ := cmd.Flags().GetString("path")
			project, _ := cmd.Flags().GetString("project")
			since, _ := cmd.Flags().GetString("since")
			return runQueryLexical(args[0], path, project, since, k)
		},
	}
	grepCmd.Flags().Int("k", 50, "max results")
	grepCmd.Flags().String("path", "", "filter by path substring")
	grepCmd.Flags().String("project", "", "restrict to a workspace project (by name)")
	grepCmd.Flags().String("since", "", "restrict to files changed between <ref>...HEAD")

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
			focus, _ := cmd.Flags().GetString("focus")
			return runQueryNeighborhood(args[0], project, depth, dir, focus)
		},
	}
	neighborCmd.Flags().Int("depth", 2, "traversal depth (max 5)")
	neighborCmd.Flags().String("direction", "both", "out | in | both")
	neighborCmd.Flags().String("project", "", "restrict seed lookup to a workspace project (traversal remains global)")
	neighborCmd.Flags().String("focus", "", "v2.4 lexical focus filter — drop unmatched leaves from the result")

	impactCmd := &cobra.Command{
		Use:   "impact <symbol>",
		Short: "Transitive inbound closure around a symbol, ranked by distance (v1.6)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, _ := cmd.Flags().GetString("kind")
			depth, _ := cmd.Flags().GetInt("depth")
			project, _ := cmd.Flags().GetString("project")
			since, _ := cmd.Flags().GetString("since")
			return runQueryImpact(args[0], kind, project, since, depth)
		},
	}
	impactCmd.Flags().String("kind", "", "filter callers by kind (e.g. 'method', 'function')")
	impactCmd.Flags().Int("depth", 5, "max traversal depth (max 10)")
	impactCmd.Flags().String("project", "", "restrict reported callers to a workspace project")
	impactCmd.Flags().String("since", "", "restrict reported callers to files changed between <ref>...HEAD")

	pathCmd := &cobra.Command{
		Use:   "path <from> <to>",
		Short: "Shortest outbound call paths between two symbols (v1.6)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			depth, _ := cmd.Flags().GetInt("depth")
			k, _ := cmd.Flags().GetInt("k")
			project, _ := cmd.Flags().GetString("project")
			return runQueryCriticalPath(args[0], args[1], project, depth, k)
		},
	}
	pathCmd.Flags().Int("depth", 8, "max path length (max 8)")
	pathCmd.Flags().Int("k", 5, "max paths to return")
	pathCmd.Flags().String("project", "", "restrict seed lookups to a workspace project (traversal remains global)")

	cmd.AddCommand(
		findCmd,
		refsCmd,
		filesCmd,
		outlineCmd,
		grepCmd,
		summaryCmd,
		neighborCmd,
		impactCmd,
		pathCmd,
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
					since, _ := cmd.Flags().GetString("since")
					return runQuerySearch(args[0], k, kind, path, project, since)
				},
			}
			c.Flags().Int("k", 10, "number of results")
			c.Flags().String("kind", "", "filter by kind")
			c.Flags().String("path", "", "filter by path substring")
			c.Flags().String("project", "", "restrict to a workspace project (by name)")
			c.Flags().String("since", "", "restrict to files changed between <ref>...HEAD")
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

// resolveCLISince turns the offline-path `--since <ref>` flag into the
// resolved path list. Empty ref → nil (unscoped). Any git error
// surfaces verbatim; the CLI prefers a loud failure over a silently
// unfiltered result.
func resolveCLISince(ctx context.Context, rc repoCtx, since string) ([]string, error) {
	if since == "" {
		return nil, nil
	}
	return gitref.ResolveSince(ctx, rc.Root, since)
}

func runQueryFind(name, kind, project, since, focus string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var result query.FindSymbolResult
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodFindSymbol, ipc.FindSymbolParams{Name: name, Kind: kind, Project: project, Since: since, Limit: limit, Focus: focus}, &result); err != nil {
			return err
		}
	} else {
		paths, err := resolveCLISince(ctx, rc, since)
		if err != nil {
			return err
		}
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		result, err = r.FindSymbol(ctx, name, kind, project, limit, paths, focus)
		if err != nil {
			return err
		}
	}
	hits := result.Matches
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "no matches")
		for _, h := range result.Hints {
			fmt.Fprintf(os.Stderr, "  hint: %s\n", h)
		}
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

func runQueryRefs(target, project, since string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.ReferenceHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodGetReferences, ipc.GetReferencesParams{Target: target, Project: project, Since: since, Limit: limit}, &hits); err != nil {
			return err
		}
	} else {
		paths, err := resolveCLISince(ctx, rc, since)
		if err != nil {
			return err
		}
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.GetReferences(ctx, target, project, limit, paths)
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

func runQueryFiles(nameContains, language, project, since string, limit int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.FileHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodListFiles, ipc.ListFilesParams{Language: language, NameContains: nameContains, Project: project, Since: since, Limit: limit}, &hits); err != nil {
			return err
		}
	} else {
		paths, err := resolveCLISince(ctx, rc, since)
		if err != nil {
			return err
		}
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.ListFiles(ctx, language, nameContains, project, limit, paths)
		if err != nil {
			return err
		}
	}
	for _, h := range hits {
		fmt.Printf("%s  [%s]  %d symbols  %d bytes\n", h.Path, h.Language, h.SymbolCount, h.SizeBytes)
	}
	return nil
}

func runQueryLexical(pattern, pathContains, project, since string, k int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.LexicalHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodSearchLexical, ipc.SearchLexicalParams{Pattern: pattern, PathContains: pathContains, Project: project, Since: since, K: k}, &hits); err != nil {
			return err
		}
	} else {
		paths, err := resolveCLISince(ctx, rc, since)
		if err != nil {
			return err
		}
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		hits, err = r.SearchLexical(ctx, pattern, pathContains, project, k, rc.Root, paths)
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

func runQueryNeighborhood(target, project string, depth int, direction, focus string) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var nb query.Neighborhood
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodGetNeighborhood, ipc.GetNeighborhoodParams{Target: target, Project: project, Depth: depth, Direction: direction, Focus: focus}, &nb); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		nb, err = r.GetNeighborhood(ctx, target, project, depth, query.Direction(direction), focus)
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

func runQueryImpact(target, kind, project, since string, depth int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var imp query.Impact
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodImpactAnalysis, ipc.ImpactAnalysisParams{Target: target, Kind: kind, Depth: depth, Project: project, Since: since}, &imp); err != nil {
			return err
		}
	} else {
		paths, err := resolveCLISince(ctx, rc, since)
		if err != nil {
			return err
		}
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		imp, err = r.ImpactAnalysis(ctx, target, kind, project, depth, paths)
		if err != nil {
			return err
		}
	}
	for _, note := range imp.Notes {
		fmt.Fprintf(os.Stderr, "note: %s\n", note)
	}
	fmt.Printf("seed: %s  (%s:%d)\n", imp.Seed.Qualified, imp.Seed.Path, imp.Seed.StartLine)
	if len(imp.Hits) == 0 {
		fmt.Fprintln(os.Stderr, "no callers found within depth")
		return nil
	}
	for _, h := range imp.Hits {
		fmt.Printf("  d=%d  [%s]  %s  (%s:%d)\n", h.Distance, h.Kind, h.Qualified, h.Path, h.StartLine)
	}
	return nil
}

func runQueryCriticalPath(from, to, project string, depth, k int) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var cp query.CriticalPathResult
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodCriticalPath, ipc.CriticalPathParams{From: from, To: to, Depth: depth, K: k, Project: project}, &cp); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		cp, err = r.CriticalPath(ctx, from, to, project, depth, k)
		if err != nil {
			return err
		}
	}
	for _, note := range cp.Notes {
		fmt.Fprintf(os.Stderr, "note: %s\n", note)
	}
	fmt.Printf("from: %s  (%s:%d)\n", cp.From.Qualified, cp.From.Path, cp.From.StartLine)
	fmt.Printf("to:   %s  (%s:%d)\n", cp.To.Qualified, cp.To.Path, cp.To.StartLine)
	if len(cp.Paths) == 0 {
		fmt.Fprintln(os.Stderr, "no path found within depth")
		return nil
	}
	for i, path := range cp.Paths {
		fmt.Printf("path %d (%d hop%s):\n", i+1, len(path)-1, plural(len(path)-1))
		for j, v := range path {
			prefix := "  "
			if j > 0 {
				prefix = "  -> "
			}
			fmt.Printf("%s%s  (%s:%d)\n", prefix, v.Qualified, v.Path, v.StartLine)
		}
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func runQuerySearch(q string, k int, kind, pathContains, project, since string) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var hits []query.SemanticHit
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodSearchSemantic, ipc.SearchSemanticParams{Query: q, K: k, Kind: kind, PathContains: pathContains, Project: project, Since: since}, &hits); err != nil {
			return err
		}
	} else {
		// Offline fallback: open the DB read-side ourselves. This can't happen
		// if the daemon is running (SQLite WAL allows concurrent readers).
		paths, err := resolveCLISince(ctx, rc, since)
		if err != nil {
			return err
		}
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
		hits, err = s.SearchSemantic(ctx, q, k, kind, pathContains, project, paths)
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

func runQueryOutline(path, focus string) error {
	ctx := context.Background()
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	var items []query.FileOutlineItem
	if c, ok := daemonClient(rc); ok {
		if err := c.Call(ipc.MethodGetFileOutline, ipc.GetFileOutlineParams{Path: path, Focus: focus}, &items); err != nil {
			return err
		}
	} else {
		ix, err := openIndex(rc)
		if err != nil {
			return err
		}
		defer ix.Close()
		r := query.NewReader(ix.DB())
		items, err = r.GetFileOutline(ctx, path, focus)
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

// runStatsTelemetry renders a per-tool histogram from the telemetry
// JSONL log. Friendly hint when the log is missing or empty so users
// who turned telemetry on but haven't generated traffic yet understand
// what they're seeing.
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
			rep, err := doctor.Run(ctx, r, rc.Cfg.Embedder.Provider, doctor.ThresholdsFromConfig(rc.Cfg), rc.Root)
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
				// Offer session-tracking hooks immediately after MCP registration.
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
		rep, err := doctor.Run(ctx, r, "", doctor.ThresholdsFromConfig(config.Default()), root)
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
func buildConfig(langs []string, projects []config.ProjectConfig, telemetry bool, watcherBackend string) config.Config {
	cfg := config.Default()
	cfg.Languages = langs
	cfg.Projects = projects
	cfg.Telemetry.Enabled = telemetry
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

	// ── Step 2: git post-commit hook ─────────────────────────────────
	if inRepo {
		wizard.Step("Git post-commit hook…")
		hookPath := filepath.Join(root, ".git", "hooks", "post-commit")
		if _, err := os.Stat(hookPath); err == nil {
			if wizard.YN("  Remove .git/hooks/post-commit (or restore .mycelium-backup)?", true, yes) {
				if dryRun {
					wizard.Skip("(dry-run) would uninstall mycelium hook")
				} else {
					removed, err := hook.UninstallPostCommit(root)
					if err != nil {
						wizard.Warn("hook: " + err.Error())
					} else if removed {
						wizard.Done("removed (or restored backup of) post-commit hook")
					} else {
						wizard.Skip("post-commit hook is not managed by mycelium — left untouched")
					}
				}
			}
		} else {
			wizard.Skip("no .git/hooks/post-commit found")
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
// currently-running binary, deduplicated by path. Symlinks and their
// targets are both included so the user can decide on each. Order is:
// running binary first (followed by its target if it's a symlink), then
// PATH entries in PATH order.
func findMycoBinaries(runningPath string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
		// Include one level of symlink target so the user can clear both.
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
// .claude/settings.local.json hooks map. An entry is "ours" when its
// command string contains "myco session " — matching every command init
// writes (session start --auto, session track, session annotate --stdin).
// Foreign hook entries are preserved. Returns true if any change was made.
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

func newDaemonCmd() *cobra.Command {
	var backendOverride string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived indexer + query server for this repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runDaemon(ctx, backendOverride)
		},
	}
	cmd.Flags().StringVar(&backendOverride, "watcher-backend", "",
		"override watcher backend (fsnotify | watchman); defaults to config")
	return cmd
}

func runDaemon(ctx context.Context, backendOverride string) error {
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
		Documents: buildDocumentRegistry(),
	}

	// Catch-up scan before accepting connections so the index reflects any
	// changes that happened while the daemon was down.
	if rep, err := p.RunOnce(ctx); err != nil {
		return fmt.Errorf("catch-up scan: %w", err)
	} else {
		fmt.Fprintf(os.Stderr, "[daemon] catch-up: scanned=%d changed=%d duration=%s\n",
			rep.FilesScanned, rep.FilesChanged, rep.Duration)
	}

	backend := rc.Cfg.Watcher.Backend
	if backendOverride != "" {
		backend = backendOverride
	}
	wat, err := watch.New(watch.Options{
		Root:          rc.Root,
		Include:       rc.Cfg.Include,
		Exclude:       rc.Cfg.Exclude,
		MaxFileSizeKB: rc.Cfg.Index.MaxFileSizeKB,
		DebounceMS:    rc.Cfg.Watcher.DebounceMS,
		CoalesceMS:    rc.Cfg.Watcher.CoalesceMS,
		Backend:       backend,
	})
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

	// v2.2: opt-in telemetry. Default-off; when enabled, the daemon
	// records per-call timing/byte stats to `.mycelium/telemetry.jsonl`
	// (or the configured path). Open failures fall back to Disabled
	// rather than aborting daemon startup — observability shouldn't
	// gate availability.
	var rec telemetry.Recorder = telemetry.Disabled{}
	if rc.Cfg.Telemetry.Enabled {
		path := rc.Cfg.Telemetry.Path
		if path == "" {
			path = filepath.Join(rc.Root, ".mycelium", "telemetry.jsonl")
		}
		fr, err := telemetry.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[daemon] telemetry disabled: %v\n", err)
		} else {
			sessionFile := filepath.Join(rc.Root, ".mycelium", "current_session.json")
			fr.SetSessionFile(sessionFile)
			rec = fr
			fmt.Fprintf(os.Stderr, "[daemon] telemetry on: %s\n", path)
			defer fr.Close()
		}
	}

	d := &daemon.Daemon{
		Pipeline:  p,
		Reader:    query.NewReader(ix.DB()),
		Embedder:  emb,
		Watcher:   wat,
		Socket:    rc.Root + "/" + rc.Cfg.Daemon.Socket,
		RepoRoot:  rc.Root,
		VSSTable:  ix.VSSTableName(),
		Telemetry: rec,
	}

	// v2.5 incremental skills regen: only wired when the user has
	// previously compiled the tree (i.e. .mycelium/skills/ exists).
	// Avoids surprising users who never opted into the skills feature
	// with a regenerated tree on first daemon start.
	skillsDir := filepath.Join(rc.Root, ".mycelium", "skills")
	if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
		reader := query.NewReader(ix.DB())
		d.SkillsRegen = func(ctx context.Context, packages []string) error {
			return skills.RegenerateAffected(ctx, reader, skills.Options{
				OutDir: skillsDir,
				Store:  ix,
			}, packages)
		}
		fmt.Fprintf(os.Stderr, "[daemon] skills regen on: %s\n", skillsDir)
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

// newSkillsCmd is the v2.3 entrypoint for generating the Markdown
// skills tree under .mycelium/skills/. The tree is the v3 "agent-
// native" pillar: an on-disk filesystem of structural facts agents
// can navigate with only Read.
//
// Subcommand group leaves room for `myco skills outline` and similar
// in later milestones.
func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Generate / inspect the on-disk skills tree (v2.3)",
	}
	cmd.AddCommand(newSkillsCompileCmd())
	return cmd
}

func newSkillsCompileCmd() *cobra.Command {
	var (
		outDir       string
		pkgFilter    string
		aspectFilter string
		status       bool
		incremental  bool
	)
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Regenerate the skills tree (default: .mycelium/skills/)",
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

			out := outDir
			if out == "" {
				out = filepath.Join(rc.Root, ".mycelium", "skills")
			}
			opts := skills.Options{
				OutDir:        out,
				PackageFilter: pkgFilter,
				AspectFilter:  aspectFilter,
			}
			// --status renders + hashes every output but neither writes
			// nor mutates the store: we want to know what *would* change
			// against the real on-disk tree, not against a temp dir.
			if status {
				opts.Store = ix
				opts.DryRun = true
				stats := skills.Stats{}
				opts.Stats = &stats
				if err := skills.Compile(ctx, query.NewReader(ix.DB()), opts); err != nil {
					return err
				}
				fmt.Printf("status: %d rendered, %d unchanged, %d would change\n",
					stats.Rendered, stats.Skipped, stats.Written)
				return nil
			}
			// --incremental writes through the index store so unchanged
			// files are skipped (v2.5 default behaviour for the daemon).
			if incremental {
				opts.Store = ix
				stats := skills.Stats{}
				opts.Stats = &stats
				start := time.Now()
				if err := skills.Compile(ctx, query.NewReader(ix.DB()), opts); err != nil {
					return err
				}
				fmt.Printf("compiled skills tree to %s (%s; rendered=%d wrote=%d skipped=%d)\n",
					out, time.Since(start).Round(time.Millisecond), stats.Rendered, stats.Written, stats.Skipped)
				return nil
			}
			start := time.Now()
			if err := skills.Compile(ctx, query.NewReader(ix.DB()), opts); err != nil {
				return err
			}
			fmt.Printf("compiled skills tree to %s (%s)\n", out, time.Since(start).Round(time.Millisecond))
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "", "output directory (default: .mycelium/skills/)")
	cmd.Flags().StringVar(&pkgFilter, "package", "", "regenerate only this package directory (skips root index + aspects)")
	cmd.Flags().StringVar(&aspectFilter, "aspect", "", "regenerate only this aspect (skips packages + root index)")
	cmd.Flags().BoolVar(&status, "status", false, "report how many files would change without writing (v2.5)")
	cmd.Flags().BoolVar(&incremental, "incremental", false, "use the v2.5 hash gate: only rewrite files whose rendered bytes differ")
	return cmd
}


// newReadCmd is the v2.4 `myco read` subcommand: it returns a single
// indexed file with non-focus-matching symbols collapsed to one-line
// markers. Empty `--focus` returns the file in full (a daemon-mediated
// alternative to `cat`).
func newReadCmd() *cobra.Command {
	var (
		focus    string
		showHdr  bool
	)
	cmd := &cobra.Command{
		Use:   "read <path>",
		Short: "Read one indexed file with non-matching symbols collapsed (v2.4)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			path := args[0]
			var fr query.FocusedRead
			if c, ok := daemonClient(rc); ok {
				if err := c.Call(ipc.MethodReadFocused, ipc.ReadFocusedParams{Path: path, Focus: focus}, &fr); err != nil {
					return err
				}
			} else {
				ix, err := openIndex(rc)
				if err != nil {
					return err
				}
				defer ix.Close()
				r := query.NewReader(ix.DB())
				fr, err = r.ReadFocused(ctx, rc.Root, path, focus)
				if err != nil {
					return err
				}
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
			return nil
		},
	}
	cmd.Flags().StringVar(&focus, "focus", "", "lexical focus hint; empty returns the full file")
	cmd.Flags().BoolVar(&showHdr, "stats", false, "print collapse stats to stderr")
	return cmd
}

// openIndex opens the repo's SQLite index and applies the vector-extension
// config. Callers don't need to care whether sqlite-vec loaded — the
// returned Index transparently handles both fast and fallback paths.
//
// Resolution order for the extension path:
//
//   1. config.index.vector.extension_path (explicit user override).
//   2. The release-tarball-bundled library at <exe-dir>/lib/vec0.*.
//   3. Empty — brute-force cosine fallback.
//
// Release builds get (2) for free; users on `go install` builds (no
// bundled library) and CI test envs land at (3) unless they set (1).
func openIndex(rc repoCtx) (*index.Index, error) {
	extPath := rc.Cfg.Index.Vector.ExtensionPath
	if extPath == "" {
		extPath = index.DefaultExtensionPath()
	}
	return index.OpenWithExtension(rc.Root+"/"+rc.Cfg.Index.Path, extPath)
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
// buildDocumentRegistry assembles the v3.3 document parsers. The
// three kinds (i18n_json, package_json_deps, go_mod_requires) are
// always registered — they only fire when the walker encounters
// matching files, so a code-only repo pays nothing for them. Future
// pluggability (per-repo enable/disable, custom kinds) is out of
// scope until a field test motivates it.
func buildDocumentRegistry() *document.Registry {
	r := document.NewRegistry()
	r.Register(document.NewI18NJSON())
	r.Register(document.NewPackageJSON())
	r.Register(document.NewGoMod())
	return r
}

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

// ─── myco session ─────────────────────────────────────────────────────────────

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Measure and export agent session telemetry (v2.6)",
	}
	cmd.AddCommand(
		newSessionStartCmd(),
		newSessionListCmd(),
		newSessionExportCmd(),
		newSessionCompareCmd(),
		newSessionAnnotateCmd(),
		newSessionTrackCmd(),
		newSessionTranscriptCmd(),
		newSessionHooksCmd(),
	)
	return cmd
}

// transcriptFor resolves the Claude Code transcript path for a session:
// uses the stored TranscriptPath first, then falls back to the conventional
// ~/.claude/projects/<slug>/<claude_session_id>.jsonl derivation.
func transcriptFor(rep telemetry.SessionReport, repoRoot string) string {
	if rep.Session.TranscriptPath != "" {
		return rep.Session.TranscriptPath
	}
	return telemetry.TranscriptPathFromSessionID(repoRoot, rep.Session.ClaudeSessionID)
}

func sessionPaths(rc repoCtx) (jsonlPath, sessionFilePath, hookMetaDir string) {
	base := filepath.Join(rc.Root, ".mycelium")
	jsonlPath = rc.Cfg.Telemetry.Path
	if jsonlPath == "" {
		jsonlPath = filepath.Join(base, "telemetry.jsonl")
	}
	sessionFilePath = filepath.Join(base, "current_session.json")
	hookMetaDir = base
	return
}

func newSessionStartCmd() *cobra.Command {
	var auto bool
	cmd := &cobra.Command{
		Use:   "start [name]",
		Short: "Begin a new named session (stamps all subsequent telemetry calls with its ID)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			if !rc.Cfg.Telemetry.Enabled {
				fmt.Fprintln(os.Stderr,
					"hint: telemetry.enabled is false — enable it in .mycelium.yml and restart the daemon.")
			}
			jsonlPath, sessionFilePath, _ := sessionPaths(rc)

			name := strings.Join(args, " ")
			var claudeSessionID, transcriptPath string
			if auto {
				// Parse all useful fields from the hook stdin payload.
				hook := telemetry.ParseHookStdin(os.Stdin)
				if hook.Name != "" && name == "" {
					name = hook.Name
				}
				claudeSessionID = hook.ClaudeSessionID
				transcriptPath = hook.TranscriptPath
			}

			meta, err := telemetry.StartSession(jsonlPath, sessionFilePath, name, claudeSessionID, transcriptPath)
			if err != nil {
				return err
			}
			fmt.Printf("started  %s  %q\n", meta.ID, meta.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&auto, "auto", false,
		"read name hint from Claude Code hook stdin; for UserPromptSubmit hooks")
	return cmd
}

func newSessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List recorded sessions with call counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			jsonlPath, _, _ := sessionPaths(rc)
			reports, err := telemetry.ListSessions(jsonlPath)
			if err != nil {
				return err
			}
			if len(reports) == 0 {
				fmt.Fprintln(os.Stderr, "no sessions recorded (run `myco session start` first)")
				return nil
			}
			// Width 38 fits both `ses_<date>_<8rand>` (20 chars) and a
			// Claude UUID (36 chars) without truncation.
			fmt.Printf("%-38s  %-20s  %-19s  %6s  %12s\n",
				"ID", "name", "started", "calls", "out_bytes")
			for _, r := range reports {
				fmt.Printf("%-38s  %-20s  %-19s  %6d  %12s\n",
					r.Session.ID,
					truncate(r.Session.Name, 20),
					r.Session.StartedAt.Format("2006-01-02 15:04:05"),
					r.TotalCalls,
					humanBytes(r.OutputBytes),
				)
			}
			return nil
		},
	}
}

func newSessionExportCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "export <session-id>",
		Short: "Render a full report for one session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			jsonlPath, _, hookMetaDir := sessionPaths(rc)
			rep, err := telemetry.AggregateSession(jsonlPath, args[0])
			if err != nil {
				return err
			}
			hook, hasHook := telemetry.ReadHookMeta(hookMetaDir, args[0])
			ext, _ := telemetry.SummarizeExternal(telemetry.ExternalPath(hookMetaDir, args[0]))
			ts, _ := telemetry.ParseTranscript(transcriptFor(rep, rc.Root))
			switch format {
			case "json":
				return printSessionJSON(rep, hook, hasHook, ext, ts)
			case "markdown", "md":
				printSessionMarkdown(rep, hook, hasHook, ext, ts)
			default:
				printSessionTable(rep, hook, hasHook, ext, ts)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "table", "output format: table | json | markdown")
	return cmd
}

func newSessionCompareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compare <session-a> <session-b>",
		Short: "Side-by-side diff of two sessions",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			jsonlPath, _, hookMetaDir := sessionPaths(rc)
			a, err := telemetry.AggregateSession(jsonlPath, args[0])
			if err != nil {
				return fmt.Errorf("session A: %w", err)
			}
			b, err := telemetry.AggregateSession(jsonlPath, args[1])
			if err != nil {
				return fmt.Errorf("session B: %w", err)
			}
			hookA, _ := telemetry.ReadHookMeta(hookMetaDir, args[0])
			hookB, _ := telemetry.ReadHookMeta(hookMetaDir, args[1])
			extA, _ := telemetry.SummarizeExternal(telemetry.ExternalPath(hookMetaDir, args[0]))
			extB, _ := telemetry.SummarizeExternal(telemetry.ExternalPath(hookMetaDir, args[1]))
			tsA, _ := telemetry.ParseTranscript(transcriptFor(a, rc.Root))
			tsB, _ := telemetry.ParseTranscript(transcriptFor(b, rc.Root))
			printSessionCompare(a, b, hookA, hookB, extA, extB, tsA, tsB)
			return nil
		},
	}
}

// newSessionTrackCmd is called by the Claude Code PostToolUse hook after
// every tool call. It records non-myco tool uses so the session export
// can show how often the agent fell back to grep/Read/etc. instead of
// using myco — the key signal for evaluating myco's coverage.
func newSessionTrackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "track",
		Short: "Record a non-myco tool call from a PostToolUse hook (reads JSON from stdin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			_, sessionFilePath, hookMetaDir := sessionPaths(rc)
			meta, ok := telemetry.LoadCurrentSession(sessionFilePath)
			if !ok {
				// No active session — silently exit. This is normal at
				// startup before the first session start.
				return nil
			}
			rec, ok := telemetry.ParsePostToolUse(os.Stdin, meta.ID)
			if !ok {
				// MCP call or unrecognised payload — skip without error.
				return nil
			}
			extPath := telemetry.ExternalPath(hookMetaDir, meta.ID)
			return telemetry.AppendExternal(extPath, rec)
		},
	}
}

// newSessionTranscriptCmd renders the Claude Code conversation transcript
// linked to a session. Two modes:
//
//   - default ("fallbacks"): shows only the agent reasoning + tool call around
//     each fallback (grep/Read/etc.) — the decision points where the agent
//     chose not to use myco. This is the primary evaluation signal.
//   - --full: renders the complete conversation (messages + all tool calls),
//     equivalent to the Python extract_chat.py script.
//
// The <session-id> arg may be omitted when --transcript <path> is given —
// useful for rendering a JSONL you already have a path to, without needing
// myco to have tracked the session.
func newSessionTranscriptCmd() *cobra.Command {
	var full bool
	var explicitPath string
	cmd := &cobra.Command{
		Use:   "transcript [session-id]",
		Short: "Render the Claude conversation linked to a session (fallback decision points by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			jsonlPath, _, _ := sessionPaths(rc)

			tpath := explicitPath
			sessionID := ""
			if len(args) > 0 {
				sessionID = args[0]
			}
			if tpath == "" && sessionID == "" {
				return fmt.Errorf("provide a session ID, --transcript <path>, or both")
			}

			// If we have a session ID, try to resolve its transcript link;
			// don't bail if the session isn't known and a path was given.
			if tpath == "" {
				rep, err := telemetry.AggregateSession(jsonlPath, sessionID)
				if err != nil {
					return err
				}
				tpath = transcriptFor(rep, rc.Root)
				if tpath == "" {
					candidates := telemetry.DiscoverTranscripts(rc.Root, rep.Session.StartedAt)
					switch len(candidates) {
					case 0:
						fmt.Fprintf(os.Stderr,
							"No transcript found for session %s.\n"+
								"  Try one of:\n"+
								"  1. Pass the path explicitly: myco session transcript --transcript <path>\n"+
								"  2. Browse manually:           ls -lt ~/.claude/projects/%s/\n",
							sessionID, telemetry.ClaudeProjectSlug(rc.Root))
						return fmt.Errorf("transcript not found for session %s", sessionID)
					case 1:
						tpath = candidates[0]
						fmt.Fprintf(os.Stderr, "auto-discovered transcript: %s\n", tpath)
					default:
						fmt.Fprintf(os.Stderr, "Multiple transcripts found near session start time:\n")
						for i, c := range candidates {
							fmt.Fprintf(os.Stderr, "  [%d] %s\n", i+1, c)
						}
						fmt.Fprintf(os.Stderr, "Using newest. Pass --transcript <path> to override.\n")
						tpath = candidates[0]
					}
				}
			}

			events, err := telemetry.ParseTranscriptEvents(tpath)
			if err != nil {
				return fmt.Errorf("read transcript %s: %w", tpath, err)
			}
			if len(events) == 0 {
				return fmt.Errorf("transcript at %s contained no conversation turns", tpath)
			}
			if full {
				fmt.Print(telemetry.RenderTranscript(events))
			} else {
				fmt.Print(telemetry.RenderFallbackContext(events))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false,
		"render the complete conversation instead of just fallback decision points")
	cmd.Flags().StringVar(&explicitPath, "transcript", "",
		"explicit path to a Claude Code conversation JSONL file (no session ID required when set)")
	return cmd
}

// newSessionAnnotateCmd is called by the Claude Code Stop hook to attach
// token usage to the current session's sidecar file.
func newSessionAnnotateCmd() *cobra.Command {
	var (
		inputTokens  int
		outputTokens int
		sessionID    string
		fromStdin    bool
	)
	cmd := &cobra.Command{
		Use:   "annotate",
		Short: "Attach token/conversation metadata to the current session (for Stop hooks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			_, sessionFilePath, hookMetaDir := sessionPaths(rc)

			// Resolve session ID: explicit flag > stdin > current_session.json.
			sid := sessionID
			if fromStdin {
				hook := telemetry.ParseHookStdin(os.Stdin)
				if hook.InputTokens > 0 {
					inputTokens = hook.InputTokens
				}
				if hook.OutputTokens > 0 {
					outputTokens = hook.OutputTokens
				}
			}
			if sid == "" {
				meta, ok := telemetry.LoadCurrentSession(sessionFilePath)
				if !ok {
					return fmt.Errorf("no active session; pass --session <id> explicitly")
				}
				sid = meta.ID
			}

			meta := telemetry.HookMeta{
				SessionID:    sid,
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			}
			if err := telemetry.WriteHookMeta(hookMetaDir, meta); err != nil {
				return err
			}
			fmt.Printf("annotated %s  in=%d out=%d\n", sid, inputTokens, outputTokens)
			return nil
		},
	}
	cmd.Flags().IntVar(&inputTokens, "input-tokens", 0, "input token count")
	cmd.Flags().IntVar(&outputTokens, "output-tokens", 0, "output token count")
	cmd.Flags().StringVar(&sessionID, "session", "", "session ID to annotate (default: current session)")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "parse token counts from Claude Code hook JSON on stdin")
	return cmd
}

// newSessionHooksCmd writes Claude Code hook entries to the project
// .claude/settings.json so sessions start and annotate automatically.
func newSessionHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Manage Claude Code hook integration for automatic session tracking",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Write UserPromptSubmit + Stop hooks to .claude/settings.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := loadRepoCtx()
			if err != nil {
				return err
			}
			binary, err := os.Executable()
			if err != nil {
				binary = "myco"
			}
			return installSessionHooks(rc.Root, binary)
		},
	})
	return cmd
}

// installSessionHooks reads the project .claude/settings.json (creating
// it if absent), merges the two session hook entries, and writes it back.
// It does a conservative JSON merge: it reads the file as a raw map so
// it can't accidentally lose keys it doesn't know about.
func installSessionHooks(repoRoot, binary string) error {
	// Use settings.local.json — agents treat settings.json as project config
	// they can freely edit; settings.local.json is recognised as user/local
	// config and is less likely to be accidentally modified or deleted.
	settingsPath := filepath.Join(repoRoot, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}

	// Load existing settings or start from scratch.
	raw := map[string]any{}
	if b, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(b, &raw)
	}

	// Build the three hook entries we need.
	startHook := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": binary + " session start --auto",
			},
		},
	}
	trackHook := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": binary + " session track",
			},
		},
	}
	annotateHook := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": binary + " session annotate --stdin",
			},
		},
	}

	// Merge into the hooks map without clobbering unrelated entries.
	hooks, _ := raw["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	hooks["UserPromptSubmit"] = mergeHookList(hooks["UserPromptSubmit"], startHook)
	hooks["PostToolUse"] = mergeHookList(hooks["PostToolUse"], trackHook)
	hooks["Stop"] = mergeHookList(hooks["Stop"], annotateHook)
	raw["hooks"] = hooks

	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsPath, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", settingsPath)
	fmt.Println("  UserPromptSubmit → myco session start --auto    (new session per conversation)")
	fmt.Println("  PostToolUse      → myco session track            (records fallback grep/Read calls)")
	fmt.Println("  Stop             → myco session annotate --stdin (captures token counts)")
	fmt.Println()
	fmt.Println("Note: hooks are in settings.local.json — do not commit this file.")
	fmt.Println("Restart Claude Code for hooks to take effect.")
	return nil
}

// mergeHookList takes the existing value for a hook event key (which may
// be nil, a slice, or a single map) and appends our entry if not already
// present (matched by command string).
func mergeHookList(existing any, entry map[string]any) any {
	ourCmd := ""
	if hooks, ok := entry["hooks"].([]any); ok && len(hooks) > 0 {
		if h, ok := hooks[0].(map[string]any); ok {
			ourCmd, _ = h["command"].(string)
		}
	}

	var list []any
	switch v := existing.(type) {
	case []any:
		list = v
	case map[string]any:
		list = []any{v}
	}

	// Deduplicate by command string.
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if hs, ok := m["hooks"].([]any); ok {
			for _, h := range hs {
				if hm, ok := h.(map[string]any); ok {
					if cmd, _ := hm["command"].(string); cmd == ourCmd {
						return list // already present
					}
				}
			}
		}
	}
	return append(list, entry)
}

// ─── session report renderers ─────────────────────────────────────────────────

func printSessionTable(rep telemetry.SessionReport, hook telemetry.HookMeta, hasHook bool, ext []telemetry.ExternalSummary, ts telemetry.TranscriptSummary) {
	s := rep.Session
	fmt.Printf("session    %s\n", s.ID)
	fmt.Printf("name       %s\n", s.Name)
	fmt.Printf("started    %s\n", s.StartedAt.Format("2006-01-02 15:04:05"))
	if s.ClaudeSessionID != "" {
		fmt.Printf("claude_id  %s\n", s.ClaudeSessionID)
	}
	if ts.FirstUserMessage != "" {
		fmt.Printf("task       %s\n", truncate(ts.FirstUserMessage, 80))
	}
	fmt.Printf("myco_calls %d\n", rep.TotalCalls)
	fmt.Printf("in_bytes   %s\n", humanBytes(rep.InputBytes))
	fmt.Printf("out_bytes  %s\n", humanBytes(rep.OutputBytes))
	if rep.CallDuration > 0 {
		fmt.Printf("call_span  %s\n", rep.CallDuration.Round(time.Millisecond))
	}
	if hasHook && (hook.InputTokens > 0 || hook.OutputTokens > 0) {
		fmt.Printf("tokens     in=%d  out=%d\n", hook.InputTokens, hook.OutputTokens)
	}
	if ts.ToolCalls > 0 {
		fmt.Printf("turns      %d\n", ts.Turns)
		fmt.Printf("all_tools  %d  (myco=%d  edits=%d  agents=%d)\n",
			ts.ToolCalls, ts.MycoCallsFromTranscript, ts.Edits, ts.AgentSpawns)
		if ts.PlanModeUsed {
			fmt.Println("plan_mode  yes")
		}
	}
	if len(rep.Summaries) > 0 {
		fmt.Println()
		fmt.Println("── myco tools ──────────────────────────────────────────────────")
		fmt.Printf("%-22s  %6s  %6s  %12s  %8s\n", "tool", "calls", "ok", "out_bytes", "p50")
		for _, s := range rep.Summaries {
			fmt.Printf("%-22s  %6d  %6d  %12s  %8s\n",
				s.Tool, s.Count, s.OK, humanBytes(s.OutputBytes), s.P50Duration)
		}
	}
	if len(ext) > 0 {
		exploratory := telemetry.TotalExploratory(ext)
		fmt.Println()
		fmt.Printf("── fallback tools (non-myco)  exploratory=%d ────────────────────\n", exploratory)
		fmt.Printf("%-28s  %-12s  %6s\n", "tool", "category", "calls")
		for _, e := range ext {
			fmt.Printf("%-28s  %-12s  %6d\n", e.Tool, e.Category, e.Count)
		}
	} else {
		fmt.Println()
		fmt.Println("── fallback tools: none recorded ────────────────────────────────")
	}
}

func printSessionMarkdown(rep telemetry.SessionReport, hook telemetry.HookMeta, hasHook bool, ext []telemetry.ExternalSummary, ts telemetry.TranscriptSummary) {
	s := rep.Session
	fmt.Printf("## Session: %s\n\n", s.Name)
	if ts.FirstUserMessage != "" {
		fmt.Printf("> **Task:** %s\n\n", ts.FirstUserMessage)
	}
	fmt.Printf("| field | value |\n|---|---|\n")
	fmt.Printf("| ID | `%s` |\n", s.ID)
	if s.ClaudeSessionID != "" {
		fmt.Printf("| Claude session | `%s` |\n", s.ClaudeSessionID)
	}
	fmt.Printf("| Started | %s |\n", s.StartedAt.Format("2006-01-02 15:04:05"))
	if ts.Turns > 0 {
		fmt.Printf("| Conversation turns | %d |\n", ts.Turns)
		fmt.Printf("| All tool calls | %d |\n", ts.ToolCalls)
		fmt.Printf("| File edits | %d |\n", ts.Edits)
		if ts.AgentSpawns > 0 {
			fmt.Printf("| Agent spawns | %d |\n", ts.AgentSpawns)
		}
		fmt.Printf("| Plan mode | %v |\n", ts.PlanModeUsed)
	}
	fmt.Printf("| myco calls | %d |\n", rep.TotalCalls)
	fmt.Printf("| Input bytes | %s |\n", humanBytes(rep.InputBytes))
	fmt.Printf("| Output bytes | %s |\n", humanBytes(rep.OutputBytes))
	if rep.CallDuration > 0 {
		fmt.Printf("| Call span | %s |\n", rep.CallDuration.Round(time.Millisecond))
	}
	if hasHook && (hook.InputTokens > 0 || hook.OutputTokens > 0) {
		fmt.Printf("| Input tokens | %d |\n", hook.InputTokens)
		fmt.Printf("| Output tokens | %d |\n", hook.OutputTokens)
	}
	fmt.Printf("| Fallback exploratory calls | %d |\n", telemetry.TotalExploratory(ext))
	if len(rep.Summaries) > 0 {
		fmt.Printf("\n### myco tools\n\n")
		fmt.Printf("| tool | calls | ok | out_bytes | p50 |\n|---|---|---|---|---|\n")
		for _, s := range rep.Summaries {
			fmt.Printf("| %s | %d | %d | %s | %s |\n",
				s.Tool, s.Count, s.OK, humanBytes(s.OutputBytes), s.P50Duration)
		}
	}
	if len(ext) > 0 {
		fmt.Printf("\n### Fallback tools (non-myco)\n\n")
		fmt.Printf("| tool | category | calls |\n|---|---|---|\n")
		for _, e := range ext {
			fmt.Printf("| %s | %s | %d |\n", e.Tool, e.Category, e.Count)
		}
	}
}

type sessionExportJSON struct {
	Session                  telemetry.SessionMeta       `json:"session"`
	TotalMycoCalls           int                         `json:"total_myco_calls"`
	InputBytes               int64                       `json:"input_bytes"`
	OutputBytes              int64                       `json:"output_bytes"`
	CallSpanMS               int64                       `json:"call_span_ms,omitempty"`
	InputTokens              int                         `json:"input_tokens,omitempty"`
	OutputTokens             int                         `json:"output_tokens,omitempty"`
	FallbackExploratoryTotal int                         `json:"fallback_exploratory_total"`
	MycoTools                []telemetry.Summary         `json:"myco_tools"`
	FallbackTools            []telemetry.ExternalSummary `json:"fallback_tools"`
	Transcript               *telemetry.TranscriptSummary `json:"transcript,omitempty"`
}

func printSessionJSON(rep telemetry.SessionReport, hook telemetry.HookMeta, hasHook bool, ext []telemetry.ExternalSummary, ts telemetry.TranscriptSummary) error {
	out := sessionExportJSON{
		Session:                  rep.Session,
		TotalMycoCalls:           rep.TotalCalls,
		InputBytes:               rep.InputBytes,
		OutputBytes:              rep.OutputBytes,
		MycoTools:                rep.Summaries,
		FallbackTools:            ext,
		FallbackExploratoryTotal: telemetry.TotalExploratory(ext),
	}
	if rep.CallDuration > 0 {
		out.CallSpanMS = rep.CallDuration.Milliseconds()
	}
	if hasHook {
		out.InputTokens = hook.InputTokens
		out.OutputTokens = hook.OutputTokens
	}
	if ts.ToolCalls > 0 || ts.Turns > 0 {
		out.Transcript = &ts
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func printSessionCompare(a, b telemetry.SessionReport, hookA, hookB telemetry.HookMeta, extA, extB []telemetry.ExternalSummary, tsA, tsB telemetry.TranscriptSummary) {
	nameA := truncate(a.Session.Name, 20)
	nameB := truncate(b.Session.Name, 20)
	fmt.Printf("%-26s  %20s  %20s  %10s\n", "metric", nameA, nameB, "delta")
	fmt.Println(strings.Repeat("─", 82))

	printCompareInt("myco_calls", int64(a.TotalCalls), int64(b.TotalCalls))
	printCompareBytes("myco_out_bytes", a.OutputBytes, b.OutputBytes)
	printCompareDur("myco_call_span", a.CallDuration, b.CallDuration)
	printCompareInt("fallback_exploratory", int64(telemetry.TotalExploratory(extA)), int64(telemetry.TotalExploratory(extB)))
	if tsA.ToolCalls > 0 || tsB.ToolCalls > 0 {
		printCompareInt("turns", int64(tsA.Turns), int64(tsB.Turns))
		printCompareInt("all_tool_calls", int64(tsA.ToolCalls), int64(tsB.ToolCalls))
		printCompareInt("edits", int64(tsA.Edits), int64(tsB.Edits))
		printCompareInt("agent_spawns", int64(tsA.AgentSpawns), int64(tsB.AgentSpawns))
	}

	if hookA.InputTokens > 0 || hookB.InputTokens > 0 {
		printCompareInt("input_tokens", int64(hookA.InputTokens), int64(hookB.InputTokens))
	}
	if hookA.OutputTokens > 0 || hookB.OutputTokens > 0 {
		printCompareInt("output_tokens", int64(hookA.OutputTokens), int64(hookB.OutputTokens))
	}

	// myco per-tool breakdown.
	allMyco := map[string]struct{}{}
	byToolA := map[string]telemetry.Summary{}
	byToolB := map[string]telemetry.Summary{}
	for _, s := range a.Summaries {
		allMyco[s.Tool] = struct{}{}
		byToolA[s.Tool] = s
	}
	for _, s := range b.Summaries {
		allMyco[s.Tool] = struct{}{}
		byToolB[s.Tool] = s
	}
	if len(allMyco) > 0 {
		fmt.Println()
		fmt.Printf("%-26s  %20s  %20s  %10s\n", "myco tool", "calls-A", "calls-B", "delta")
		fmt.Println(strings.Repeat("─", 82))
		tools := make([]string, 0, len(allMyco))
		for t := range allMyco {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		for _, t := range tools {
			ca := int64(byToolA[t].Count)
			cb := int64(byToolB[t].Count)
			d := cb - ca
			sign := "+"
			if d < 0 {
				sign = ""
			}
			fmt.Printf("%-26s  %20d  %20d  %s%d\n", t, ca, cb, sign, d)
		}
	}

	// Fallback tool breakdown.
	allFallback := map[string]struct{}{}
	byExtA := map[string]int{}
	byExtB := map[string]int{}
	for _, e := range extA {
		allFallback[e.Tool] = struct{}{}
		byExtA[e.Tool] = e.Count
	}
	for _, e := range extB {
		allFallback[e.Tool] = struct{}{}
		byExtB[e.Tool] = e.Count
	}
	if len(allFallback) > 0 {
		fmt.Println()
		fmt.Printf("%-26s  %20s  %20s  %10s\n", "fallback tool", "calls-A", "calls-B", "delta")
		fmt.Println(strings.Repeat("─", 82))
		tools := make([]string, 0, len(allFallback))
		for t := range allFallback {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		for _, t := range tools {
			ca := int64(byExtA[t])
			cb := int64(byExtB[t])
			d := cb - ca
			sign := "+"
			if d < 0 {
				sign = ""
			}
			fmt.Printf("%-26s  %20d  %20d  %s%d\n", t, ca, cb, sign, d)
		}
	}
}

func printCompareInt(label string, a, b int64) {
	d := b - a
	sign := "+"
	if d < 0 {
		sign = ""
	}
	fmt.Printf("%-22s  %20d  %20d  %s%d\n", label, a, b, sign, d)
}

func printCompareBytes(label string, a, b int64) {
	d := b - a
	sign := "+"
	if d < 0 {
		sign = ""
	}
	fmt.Printf("%-22s  %20s  %20s  %s%s\n", label, humanBytes(a), humanBytes(b), sign, humanBytes(abs64(d)))
}

func printCompareDur(label string, a, b time.Duration) {
	d := b - a
	sign := "+"
	if d < 0 {
		sign = ""
	}
	fmt.Printf("%-22s  %20s  %20s  %s%s\n", label,
		a.Round(time.Millisecond), b.Round(time.Millisecond), sign, d.Round(time.Millisecond))
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
