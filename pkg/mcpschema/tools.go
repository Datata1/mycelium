package mcpschema

// ProtocolVersion is the MCP spec version we implement. Update when bumping.
const ProtocolVersion = "2024-11-05"

// ServerName identifies the binary to MCP clients. Version is injected at
// build time via cmd/myco/main.go and passed in by the caller — keeping it
// here would require ldflags on this package too.
const ServerName = "mycelium"

// Tool is the subset of the MCP tool-definition shape we emit. MCP clients
// (Claude Code, Cursor) use this for tool discovery + input validation.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Tools returns the definitive tool list. Keep this in sync with the
// handlers in internal/mcp.
func Tools() []Tool {
	return []Tool{
		{
			Name:        "find_symbol",
			Description: "Locate a symbol's definition by name (exact or substring) across the indexed graph. Reach for this **before** any string search whenever you have an identifier — function, class, variable, type, interface, method. String search is for literal text; find_symbol is for navigating code structure and is faster + more accurate. Empty `Matches` may include `Hints` explaining why a filter eliminated everything (e.g. typo'd project name, kind that doesn't exist on this name).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Symbol name or substring to search for.",
					},
					"kind": map[string]any{
						"type":        "string",
						"description": "Optional kind filter: function | method | type | interface | class | var | const.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of hits (default 20).",
					},
					"project": map[string]any{
						"type":        "string",
						"description": "Optional workspace project name to scope the search to.",
					},
					"since": map[string]any{
						"type":        "string",
						"description": "Optional git ref. Restricts to files changed between <ref>...HEAD (v1.6).",
					},
					"focus": map[string]any{
						"type":        "string",
						"description": "Optional v2.4 focus hint. Drops hits that don't match and re-ranks survivors by lexical relevance to this string.",
					},
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "get_references",
			Description: "List the call-sites, imports, and other uses of a symbol. Reach for this when answering 'who calls X?' or 'where is X used?' — it's faster and more accurate than string-searching the symbol's name because it knows about resolved vs. textual refs and won't false-match on string literals or comments. Each hit is flagged resolved (graph-linked) or textual (name-match only). Pass a qualified name (e.g. `pkg.Type.Method`) when you have one — it disambiguates better than the short name.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type":        "string",
						"description": "Symbol qualified name (e.g. 'pkg.Type.Method') or short name.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of hits (default 100).",
					},
					"project": map[string]any{
						"type":        "string",
						"description": "Optional workspace project name to scope the search to.",
					},
					"since": map[string]any{
						"type":        "string",
						"description": "Optional git ref. Restricts to files changed between <ref>...HEAD (v1.6).",
					},
				},
				"required": []string{"target"},
			},
		},
		{
			Name:        "list_files",
			Description: "Enumerate indexed files, optionally filtered by language or path substring. Use this for orientation on an unfamiliar repo before zooming in with `find_symbol` or `get_file_outline`. Faster than recursive directory walks and respects the index's exclude rules so you don't see vendored/generated noise.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"language": map[string]any{
						"type":        "string",
						"description": "Filter by language: go | typescript | python.",
					},
					"name_contains": map[string]any{
						"type":        "string",
						"description": "Substring match on the path.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of files (default 500).",
					},
					"project": map[string]any{
						"type":        "string",
						"description": "Optional workspace project name to scope the search to.",
					},
					"since": map[string]any{
						"type":        "string",
						"description": "Optional git ref. Restricts to files changed between <ref>...HEAD (v1.6).",
					},
				},
			},
		},
		{
			Name:        "get_file_outline",
			Description: "Return the hierarchical symbol tree for one file. Use this to orient inside a file before reading it — the outline is far cheaper than a full read and tells you whether the file is even relevant. Pair with `read_focused` once you know which symbol you want to study.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Repo-relative path to the file.",
					},
					"focus": map[string]any{
						"type":        "string",
						"description": "Optional v2.4 focus hint. Keeps top-level items whose subtree contains a lexical match against this string.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "search_lexical",
			Description: "Ripgrep-style regex/substring search over indexed file content. Use this **only** for literal strings or regex patterns — log messages, error formats, magic constants, route literals. For symbol navigation prefer `find_symbol`; for 'who calls X' prefer `get_references`. Treating this as a general-purpose code search is a known anti-pattern: it returns text matches with no graph awareness, so refactors and renames mislead it.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Go regexp. Plain-text searches should be escaped with regexp.QuoteMeta on the client, or passed as-is if the string has no regex meta-characters.",
					},
					"path_contains": map[string]any{
						"type":        "string",
						"description": "Restrict to files whose path contains this substring.",
					},
					"k": map[string]any{
						"type":        "integer",
						"description": "Max results (default 50).",
					},
					"project": map[string]any{
						"type":        "string",
						"description": "Optional workspace project name to scope the search to.",
					},
					"since": map[string]any{
						"type":        "string",
						"description": "Optional git ref. Restricts to files changed between <ref>...HEAD (v1.6).",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        "get_file_summary",
			Description: "Structural digest of one file: exports, imports, LOC, symbol counts by kind. Use this before any file read to decide whether the file is worth opening at all — it's the cheapest possible orientation signal and answers 'what is this file?' in a single round-trip.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Repo-relative path.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "get_neighborhood",
			Description: "Walk the local call graph around a symbol — both directions in one query. Reach for this **instead of** chaining `find_symbol` + `get_references` repeatedly when you need to understand how a symbol fits into its surroundings. Direction 'out' returns callees, 'in' returns callers, 'both' unions them. Depth defaults to 2; clamped to 5 because deeper traversals on dense graphs balloon exponentially.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type":        "string",
						"description": "Qualified name (preferred) or short name of the seed symbol.",
					},
					"depth": map[string]any{
						"type":        "integer",
						"description": "Traversal depth (default 2, max 5).",
					},
					"direction": map[string]any{
						"type":        "string",
						"description": "out | in | both (default both).",
					},
					"project": map[string]any{
						"type":        "string",
						"description": "Optional workspace project — scopes the seed lookup only; traversal remains global so cross-project edges still surface.",
					},
					"focus": map[string]any{
						"type":        "string",
						"description": "Optional v2.4 focus hint. Prunes nodes that don't lexically match (the seed always survives); a note records the prune count.",
					},
				},
				"required": []string{"target"},
			},
		},
		{
			Name:        "search_semantic",
			Description: "Embedding-based search over code chunks. Use this for intent queries ('function that parses ISO dates', 'http handler for login') when you don't know the symbol name and lexical search would miss the wording. If you do know the name, prefer `find_symbol` — it's faster and exact. Requires an embedder configured; returns an error otherwise.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural-language description of what you're looking for.",
					},
					"k": map[string]any{
						"type":        "integer",
						"description": "Number of results to return (default 10).",
					},
					"kind": map[string]any{
						"type":        "string",
						"description": "Optional symbol kind filter.",
					},
					"path_contains": map[string]any{
						"type":        "string",
						"description": "Restrict to files whose path contains this substring.",
					},
					"project": map[string]any{
						"type":        "string",
						"description": "Optional workspace project name to scope the search to.",
					},
					"since": map[string]any{
						"type":        "string",
						"description": "Optional git ref. Restricts to files changed between <ref>...HEAD (v1.6).",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "impact_analysis",
			Description: "Transitive inbound closure around a symbol, ranked by distance. Reach for this when answering 'who's impacted if I change X?' — it's the right tool to scope a refactor before touching code. With a `kind` filter (e.g. 'test') it also answers 'what tests cover this?'. Returns a flat distance-sorted list; use `get_neighborhood` instead when you need the graph shape rather than a flat impact set.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type":        "string",
						"description": "Qualified name (preferred) or short name of the seed symbol.",
					},
					"kind": map[string]any{
						"type":        "string",
						"description": "Optional kind filter (e.g. 'method' or 'function') — narrows the reported callers.",
					},
					"depth": map[string]any{
						"type":        "integer",
						"description": "Traversal depth (default 5, max 10).",
					},
					"project": map[string]any{
						"type":        "string",
						"description": "Optional workspace project name to scope the reported caller set (traversal remains global).",
					},
					"since": map[string]any{
						"type":        "string",
						"description": "Optional git ref. Restricts reported callers to files changed between <ref>...HEAD.",
					},
				},
				"required": []string{"target"},
			},
		},
		{
			Name:        "critical_path",
			Description: "Up to k shortest outbound call paths from one symbol to another. Use this to answer 'how does X reach Y?' — the routes in the call graph between two specific symbols, not their general neighbourhoods. Bounded BFS over the refs graph; cycles prevented. Default k=5, max depth 8.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from": map[string]any{
						"type":        "string",
						"description": "Qualified or short name of the source (caller) symbol.",
					},
					"to": map[string]any{
						"type":        "string",
						"description": "Qualified or short name of the target (callee) symbol.",
					},
					"depth": map[string]any{
						"type":        "integer",
						"description": "Max path length (default 8, max 8).",
					},
					"k": map[string]any{
						"type":        "integer",
						"description": "Max number of paths to return (default 5).",
					},
					"project": map[string]any{
						"type":        "string",
						"description": "Optional workspace project — scopes the two seed lookups only; traversal stays global.",
					},
				},
				"required": []string{"from", "to"},
			},
		},
		{
			Name:        "read_focused",
			Description: "Read one indexed file with non-focus-matching symbols collapsed to one-line markers. Use this **instead of** the agent's general-purpose file reader whenever you know what you're looking for in the file — it cuts read bytes 30–80 % on files larger than ~5 KB by hiding the symbols that don't match the `focus` query. Empty `focus` returns the full file unchanged, so it's safe as a default file-read tool when file size is unknown. Matched symbols return in full; others become single-line markers like `// signature ...  // collapsed (lines N-M)` with the original line ranges preserved in `Expanded` for round-tripping.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Repo-relative path to the file.",
					},
					"focus": map[string]any{
						"type":        "string",
						"description": "Free-text focus hint (e.g. 'auth login flow'). Empty = expand everything.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "stats",
			Description: "Snapshot of index status: file/symbol/ref counts, languages, last-scan time, configured projects. Use this once at session start to confirm the index is fresh and the right shape before running queries — a stale or empty index will silently no-op many calls below. Cheap; no embedder required.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}
