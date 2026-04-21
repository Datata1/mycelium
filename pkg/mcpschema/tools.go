package mcpschema

// ProtocolVersion is the MCP spec version we implement. Update when bumping.
const ProtocolVersion = "2024-11-05"

// ServerName and ServerVersion identify the binary to MCP clients.
const (
	ServerName    = "mycelium"
	ServerVersion = "0.3.0-dev"
)

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
			Description: "Find symbols (functions, methods, types, etc.) by name. Supports exact and substring matches. Use this as the primary way to locate code by identifier.",
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
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "get_references",
			Description: "List the call-sites, imports, and other uses of a symbol. Accepts a qualified name (preferred) or short name. Flags each hit as resolved (graph-linked) or textual (name-match only).",
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
				},
				"required": []string{"target"},
			},
		},
		{
			Name:        "list_files",
			Description: "List indexed files, optionally filtered by language or path substring. Useful for orientation on an unfamiliar repo.",
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
				},
			},
		},
		{
			Name:        "get_file_outline",
			Description: "Return the hierarchical symbol tree for a single file. Cheap way for the agent to orient itself inside one file.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Repo-relative path to the file.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "search_semantic",
			Description: "Semantic search over code chunks. Use for fuzzy intent queries ('function that parses dates', 'http handler for login') when you don't know the symbol name. Requires an embedder configured (otherwise returns an error).",
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
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "stats",
			Description: "Index status: languages, symbol counts, refs, freshness. Useful for the agent to decide whether the index is trustworthy before running searches.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}
