package bench

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

// BuildAdaptiveCorpus probes the daemon to construct a corpus targeting
// real symbols + files in the indexed repo. Solves v4 F1/T4 — the
// hard-coded mycelium-self corpus produces ERR rows when run against
// any other repo because the targets (`ComputeSessionCost`,
// `internal/telemetry/aggregate.go`) only exist in mycelium.
//
// Probe strategy (single round-trip per step, ~3 calls total):
//
//  1. list_files (no filter, limit 50) → pick the largest indexed
//     file by symbol_count. Most symbols → best chance of yielding a
//     resolvable target for the symbol-shaped tools below.
//  2. get_file_outline on that file → pick the first non-trivial
//     symbol (name length > 2 to skip noise like `i`, `_`, etc.).
//  3. Build the corpus around (picked_file, picked_symbol).
//
// Returns an error when the probe can't pick targets (empty index,
// daemon stale, etc.); callers should fall back to MyceliumDefaultCorpus
// or surface the error to the user.
func BuildAdaptiveCorpus(client *ipc.Client) (Corpus, error) {
	picked, err := pickProbeTarget(client)
	if err != nil {
		return Corpus{}, fmt.Errorf("adaptive corpus probe: %w", err)
	}
	return buildCorpusForTarget(picked), nil
}

// probeTarget is the (file, symbol) pair the probe selected.
type probeTarget struct {
	Path     string
	Symbol   string
	Language string
}

func pickProbeTarget(client *ipc.Client) (probeTarget, error) {
	// Step 1: list_files. The daemon returns up to `limit` files;
	// we ask for 50 so the heaviest file is likely in the slice
	// without paying for the whole index.
	var files []fileSlice
	if err := client.Call(ipc.MethodListFiles, ipc.ListFilesParams{Limit: 50}, &files); err != nil {
		return probeTarget{}, fmt.Errorf("list_files: %w", err)
	}
	if len(files) == 0 {
		return probeTarget{}, fmt.Errorf("indexed repo has zero files (run `myco index` first?)")
	}

	// Sort by symbol_count desc; the file with the most symbols is the
	// best target for a `read_focused` benchmark (lots of content) AND
	// gives us many candidate symbols for find_symbol on step 2.
	sort.Slice(files, func(i, j int) bool {
		return files[i].SymbolCount > files[j].SymbolCount
	})

	// Pick the heaviest with at least one symbol — files with 0 symbols
	// (config files, plain text) can't drive the symbol-shaped tools.
	for _, f := range files {
		if f.SymbolCount == 0 {
			continue
		}
		picked := probeTarget{Path: f.Path, Language: f.Language}
		// Step 2: get_file_outline → pick a symbol from its outline.
		var outline []outlineEntry
		if err := client.Call(ipc.MethodGetFileOutline, ipc.GetFileOutlineParams{Path: f.Path}, &outline); err != nil {
			continue
		}
		for _, sym := range outline {
			if len(sym.Name) > 2 {
				picked.Symbol = sym.Name
				return picked, nil
			}
		}
	}

	return probeTarget{}, fmt.Errorf("no indexed file with usable symbols (largest had 0 or single-char names only)")
}

// buildCorpusForTarget constructs a Corpus where each Case targets the
// probed (file, symbol). Mirrors the MyceliumDefaultCorpus structure
// so the renderer + drift comparison work identically.
func buildCorpusForTarget(t probeTarget) Corpus {
	// Pick a search_lexical pattern that's likely to hit: the file's
	// basename (without extension) is a reasonable token because most
	// files are referenced by their module name elsewhere.
	base := filepath.Base(t.Path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	if base == "" {
		base = t.Symbol // fallback to the symbol name
	}

	// list_files name-contains: first 4 chars of basename so we get
	// at least one match (the file itself + likely siblings).
	listFilter := base
	if len(listFilter) > 4 {
		listFilter = listFilter[:4]
	}

	// Pick a fallback grep pattern for symbol-shaped cases. Quote
	// regex metacharacters so a Go-keyword like `package` doesn't
	// blow up grep (-rn uses ERE by default; not a real risk for
	// typical identifiers but worth being defensive).
	grepFor := t.Symbol
	listGrep := listFilter
	cmdSymbol := fmt.Sprintf("grep -rn %s --include='*' . 2>/dev/null | head -100", shellQuote(grepFor))
	cmdLexical := fmt.Sprintf("grep -rn %s --include='*' . 2>/dev/null | head -100", shellQuote(base))
	cmdList := fmt.Sprintf(
		"find . -path ./.git -prune -o -path ./.mycelium -prune -o -name %s -print",
		shellQuote("*"+listGrep+"*"),
	)

	return Corpus{
		Name: "adaptive",
		Cases: []Case{
			{Tool: "find_symbol", Method: ipc.MethodFindSymbol,
				Params:      ipc.FindSymbolParams{Name: t.Symbol},
				FallbackCmd: cmdSymbol,
				Note:        fmt.Sprintf("probe symbol: %s", t.Symbol)},
			{Tool: "get_references", Method: ipc.MethodGetReferences,
				Params:      ipc.GetReferencesParams{Target: t.Symbol},
				FallbackCmd: cmdSymbol,
				Note:        fmt.Sprintf("callers of %s", t.Symbol)},
			{Tool: "read_focused", Method: ipc.MethodReadFocused,
				Params:       ipc.ReadFocusedParams{Path: t.Path},
				FallbackFile: t.Path,
				Note:         fmt.Sprintf("preview of %s", t.Path)},
			{Tool: "get_file_outline", Method: ipc.MethodGetFileOutline,
				Params:       ipc.GetFileOutlineParams{Path: t.Path},
				FallbackFile: t.Path,
				Note:         "outline vs full Read"},
			{Tool: "get_file_summary", Method: ipc.MethodGetFileSummary,
				Params:       ipc.GetFileSummaryParams{Path: t.Path},
				FallbackFile: t.Path,
				Note:         "summary vs full Read"},
			{Tool: "search_lexical", Method: ipc.MethodSearchLexical,
				Params:      ipc.SearchLexicalParams{Pattern: base},
				FallbackCmd: cmdLexical,
				Note:        fmt.Sprintf("literal: %q", base)},
			{Tool: "list_files", Method: ipc.MethodListFiles,
				Params:      ipc.ListFilesParams{NameContains: listFilter},
				FallbackCmd: cmdList,
				Note:        fmt.Sprintf("name_contains=%q", listFilter)},
			{Tool: "impact_analysis", Method: ipc.MethodImpactAnalysis,
				Params:      ipc.ImpactAnalysisParams{Target: t.Symbol},
				FallbackCmd: cmdSymbol,
				Note:        fmt.Sprintf("impact of %s", t.Symbol)},
			{Tool: "get_neighborhood", Method: ipc.MethodGetNeighborhood,
				Params:      ipc.GetNeighborhoodParams{Target: t.Symbol, Depth: 1},
				FallbackCmd: cmdSymbol,
				Note:        fmt.Sprintf("1-hop graph around %s", t.Symbol)},
		},
	}
}

// shellQuote single-quotes a string for safe inclusion in a `bash -c`
// command. Embedded single quotes are escaped via the standard
// '\” sequence — bash's only safe way to embed a quote in a
// single-quoted string. Used to keep the adaptive grep patterns
// safe even when the probed symbol name contains shell-special chars.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fileSlice + outlineEntry mirror the daemon's response shapes for
// the methods we call. Kept narrow — we only need the fields the
// probe actually looks at, so adding new fields server-side won't
// break unmarshal.
type fileSlice struct {
	Path        string `json:"path"`
	Language    string `json:"language"`
	SymbolCount int    `json:"symbol_count"`
}

type outlineEntry struct {
	Name string `json:"name"`
}
