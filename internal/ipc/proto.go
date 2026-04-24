package ipc

import "encoding/json"

// Protocol: newline-delimited JSON request/response on a unix socket.
// Each connection handles exactly one request for simplicity; clients are
// short-lived (CLI invocations, MCP tool calls). A long-running MCP server
// will multiplex its own requests over multiple socket connections.
//
// Request:
//   {"method": "find_symbol", "params": {...}}\n
// Response:
//   {"ok": true, "result": {...}}\n          -- on success
//   {"ok": false, "error": "..."}\n          -- on failure

// Methods exposed over the socket.
const (
	MethodFindSymbol      = "find_symbol"
	MethodGetReferences   = "get_references"
	MethodListFiles       = "list_files"
	MethodGetFileOutline  = "get_file_outline"
	MethodGetFileSummary  = "get_file_summary"
	MethodGetNeighborhood = "get_neighborhood"
	MethodSearchLexical   = "search_lexical"
	MethodStats           = "stats"
	MethodReindex         = "reindex"
	MethodSearchSemantic  = "search_semantic"
	MethodImpactAnalysis  = "impact_analysis"  // v1.6
	MethodCriticalPath    = "critical_path"    // v1.6
	MethodPing            = "ping"
)

// Request is the wire shape for a client call.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the wire shape for a server reply.
type Response struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Param shapes — one per method. Kept small and typed so changes are visible
// in code review.

// The `Since` field on the 5 read-surface params is the v1.6 `--since
// <ref>` filter. The daemon resolves it to a path list via
// `internal/gitref` before calling into the reader; the reader only
// sees a resolved `[]string`.
type FindSymbolParams struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Project string `json:"project,omitempty"` // v1.5 workspace scope
	Since   string `json:"since,omitempty"`   // v1.6 PR scope (git ref)
}

type GetReferencesParams struct {
	Target  string `json:"target"`
	Limit   int    `json:"limit,omitempty"`
	Project string `json:"project,omitempty"`
	Since   string `json:"since,omitempty"`
}

type ListFilesParams struct {
	Language     string `json:"language,omitempty"`
	NameContains string `json:"name_contains,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Project      string `json:"project,omitempty"`
	Since        string `json:"since,omitempty"`
}

type GetFileOutlineParams struct {
	Path string `json:"path"`
}

type SearchSemanticParams struct {
	Query        string `json:"query"`
	K            int    `json:"k,omitempty"`
	Kind         string `json:"kind,omitempty"`
	PathContains string `json:"path_contains,omitempty"`
	Project      string `json:"project,omitempty"`
	Since        string `json:"since,omitempty"`
}

type SearchLexicalParams struct {
	Pattern      string `json:"pattern"`
	PathContains string `json:"path_contains,omitempty"`
	K            int    `json:"k,omitempty"`
	Project      string `json:"project,omitempty"`
	Since        string `json:"since,omitempty"`
}

type GetFileSummaryParams struct {
	Path string `json:"path"`
}

type GetNeighborhoodParams struct {
	Target    string `json:"target"`
	Depth     int    `json:"depth,omitempty"`
	Direction string `json:"direction,omitempty"` // out | in | both
	Project   string `json:"project,omitempty"`
}

// ImpactAnalysisParams is the v1.6 `impact_analysis` call. Depth
// defaults to 5 (server-side), max 10.
type ImpactAnalysisParams struct {
	Target  string `json:"target"`
	Kind    string `json:"kind,omitempty"`
	Depth   int    `json:"depth,omitempty"`
	Project string `json:"project,omitempty"`
	Since   string `json:"since,omitempty"`
}

// CriticalPathParams is the v1.6 `critical_path` call. Depth defaults
// to 8 (server-side max), K defaults to 5.
type CriticalPathParams struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Depth   int    `json:"depth,omitempty"`
	K       int    `json:"k,omitempty"`
	Project string `json:"project,omitempty"`
}
