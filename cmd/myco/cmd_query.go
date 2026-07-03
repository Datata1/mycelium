package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/service"
)

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
			return runQueryFind(cmd.Context(), args[0], kind, project, since, focus, limit)
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
			return runQueryRefs(cmd.Context(), args[0], project, since, limit)
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
			return runQueryFiles(cmd.Context(), name, lang, project, since, limit)
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
			return runQueryOutline(cmd.Context(), args[0], focus)
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
			return runQueryLexical(cmd.Context(), args[0], path, project, since, k)
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
			return runQuerySummary(cmd.Context(), args[0])
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
			return runQueryNeighborhood(cmd.Context(), args[0], project, depth, dir, focus)
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
			return runQueryImpact(cmd.Context(), args[0], kind, project, since, depth)
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
			return runQueryCriticalPath(cmd.Context(), args[0], args[1], project, depth, k)
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
	)
	return cmd
}

// daemonClient returns (client, true) if a daemon is reachable at the
// configured socket. Callers that can fall back to a direct DB read use
// this to avoid double-opening SQLite when the daemon owns the file.
func daemonClient(rc repoCtx) (*ipc.Client, bool) {
	c := ipc.NewClient(rc.AbsSocketPath())
	if c.IsReachable() {
		return c, true
	}
	return nil, false
}

// callRead executes one read request: over the daemon socket when the
// daemon is reachable, otherwise via a local read-only Service on the
// index. Both paths run the identical internal/service code (including
// --since resolution), so daemon-up and daemon-down output cannot drift.
// `local` is the Service method expression matching `method`.
func callRead[P, R any](ctx context.Context, rc repoCtx, method ipc.Method, params P,
	local func(*service.Service, context.Context, P) (R, error)) (R, error) {
	var out R
	if c, ok := daemonClient(rc); ok {
		err := c.Call(method, params, &out)
		return out, err
	}
	ix, err := openIndex(rc)
	if err != nil {
		return out, err
	}
	defer ix.Close()
	return local(service.NewReadOnly(ix, rc.Root, nil), ctx, params)
}

// newTopLevelFindCmd is a v4 T6 ergonomics alias: `myco find` delegates
// to runQueryFind without duplicating logic.
func newTopLevelFindCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "find <name>",
		Short:   "Find symbols by name (alias for `myco query find`)",
		Aliases: []string{"find-symbol"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, _ := cmd.Flags().GetString("kind")
			limit, _ := cmd.Flags().GetInt("limit")
			project, _ := cmd.Flags().GetString("project")
			since, _ := cmd.Flags().GetString("since")
			focus, _ := cmd.Flags().GetString("focus")
			return runQueryFind(cmd.Context(), args[0], kind, project, since, focus, limit)
		},
	}
	cmd.Flags().String("kind", "", "filter by kind: function | method | type | interface | var | const")
	cmd.Flags().Int("limit", 20, "max results")
	cmd.Flags().String("project", "", "restrict to a workspace project (by name)")
	cmd.Flags().String("since", "", "restrict to files changed between <ref>...HEAD")
	cmd.Flags().String("focus", "", "v2.4 lexical focus filter")
	return cmd
}

// newTopLevelSearchCmd is a v4 T6 ergonomics alias: `myco search` delegates
// to runQueryLexical. Pattern syntax is Go regexp.
func newTopLevelSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "search <pattern>",
		Short:   "Ripgrep-style regex search across indexed files (alias for `myco query grep`)",
		Aliases: []string{"grep"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _ := cmd.Flags().GetInt("k")
			path, _ := cmd.Flags().GetString("path")
			project, _ := cmd.Flags().GetString("project")
			since, _ := cmd.Flags().GetString("since")
			return runQueryLexical(cmd.Context(), args[0], path, project, since, k)
		},
	}
	cmd.Flags().Int("k", 50, "max results")
	cmd.Flags().String("path", "", "filter by path substring")
	cmd.Flags().String("project", "", "restrict to a workspace project (by name)")
	cmd.Flags().String("since", "", "restrict to files changed between <ref>...HEAD")
	return cmd
}

func runQueryFind(ctx context.Context, name, kind, project, since, focus string, limit int) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	result, err := callRead(ctx, rc, ipc.MethodFindSymbol,
		ipc.FindSymbolParams{Name: name, Kind: kind, Project: project, Since: since, Limit: limit, Focus: focus},
		(*service.Service).FindSymbol)
	if err != nil {
		return err
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

func runQueryRefs(ctx context.Context, target, project, since string, limit int) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	hits, err := callRead(ctx, rc, ipc.MethodGetReferences,
		ipc.GetReferencesParams{Target: target, Project: project, Since: since, Limit: limit},
		(*service.Service).GetReferences)
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

func runQueryFiles(ctx context.Context, nameContains, language, project, since string, limit int) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	hits, err := callRead(ctx, rc, ipc.MethodListFiles,
		ipc.ListFilesParams{Language: language, NameContains: nameContains, Project: project, Since: since, Limit: limit},
		(*service.Service).ListFiles)
	if err != nil {
		return err
	}
	for _, h := range hits {
		fmt.Printf("%s  [%s]  %d symbols  %d bytes\n", h.Path, h.Language, h.SymbolCount, h.SizeBytes)
	}
	return nil
}

func runQueryLexical(ctx context.Context, pattern, pathContains, project, since string, k int) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	hits, err := callRead(ctx, rc, ipc.MethodSearchLexical,
		ipc.SearchLexicalParams{Pattern: pattern, PathContains: pathContains, Project: project, Since: since, K: k},
		(*service.Service).SearchLexical)
	if err != nil {
		return err
	}
	for _, h := range hits {
		fmt.Printf("%s:%d  %s\n", h.Path, h.Line, truncate(h.Snippet, 160))
	}
	return nil
}

func runQuerySummary(ctx context.Context, path string) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	s, err := callRead(ctx, rc, ipc.MethodGetFileSummary,
		ipc.GetFileSummaryParams{Path: path},
		(*service.Service).GetFileSummary)
	if err != nil {
		return err
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

func runQueryNeighborhood(ctx context.Context, target, project string, depth int, direction, focus string) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	nb, err := callRead(ctx, rc, ipc.MethodGetNeighborhood,
		ipc.GetNeighborhoodParams{Target: target, Project: project, Depth: depth, Direction: direction, Focus: focus},
		(*service.Service).GetNeighborhood)
	if err != nil {
		return err
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

func runQueryImpact(ctx context.Context, target, kind, project, since string, depth int) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	imp, err := callRead(ctx, rc, ipc.MethodImpactAnalysis,
		ipc.ImpactAnalysisParams{Target: target, Kind: kind, Depth: depth, Project: project, Since: since},
		(*service.Service).ImpactAnalysis)
	if err != nil {
		return err
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

func runQueryCriticalPath(ctx context.Context, from, to, project string, depth, k int) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	cp, err := callRead(ctx, rc, ipc.MethodCriticalPath,
		ipc.CriticalPathParams{From: from, To: to, Depth: depth, K: k, Project: project},
		(*service.Service).CriticalPath)
	if err != nil {
		return err
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

func runQueryOutline(ctx context.Context, path, focus string) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	items, err := callRead(ctx, rc, ipc.MethodGetFileOutline,
		ipc.GetFileOutlineParams{Path: path, Focus: focus},
		(*service.Service).GetFileOutline)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(os.Stderr, "no symbols (is the path indexed?)")
		return nil
	}
	var printItem func(it ipc.FileOutlineItem, depth int)
	printItem = func(it ipc.FileOutlineItem, depth int) {
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
