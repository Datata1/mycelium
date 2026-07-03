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

// Method identifies one RPC exposed over the socket. Read-surface method
// names are identical to the MCP tool names in pkg/mcpschema.
type Method string

// Methods exposed over the socket.
const (
	MethodFindSymbol      Method = "find_symbol"
	MethodGetReferences   Method = "get_references"
	MethodListFiles       Method = "list_files"
	MethodGetFileOutline  Method = "get_file_outline"
	MethodGetFileSummary  Method = "get_file_summary"
	MethodGetNeighborhood Method = "get_neighborhood"
	MethodSearchLexical   Method = "search_lexical"
	MethodStats           Method = "stats"
	MethodReindex         Method = "reindex"
	MethodImpactAnalysis  Method = "impact_analysis"
	MethodCriticalPath    Method = "critical_path"
	MethodReadFocused     Method = "read_focused"
	MethodFindDocumentKey Method = "find_document_key"
	MethodPing            Method = "ping"
)

// Request is the wire shape for a client call.
type Request struct {
	Method Method          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the wire shape for a server reply. Code is a machine-readable
// error class (see Code* consts) so clients can branch without matching on
// the error text; it is empty for errors that fit no class.
type Response struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Code   string          `json:"code,omitempty"`
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
	Focus   string `json:"focus,omitempty"`   // v2.4 focused-reads filter
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
	Path  string `json:"path"`
	Focus string `json:"focus,omitempty"` // v2.4
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
	Focus     string `json:"focus,omitempty"` // v2.4
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

// ReadFocusedParams is the v2.4 `read_focused` call. The daemon
// resolves the file relative to the repo root and renders it with
// non-matching symbols collapsed to one-line markers.
type ReadFocusedParams struct {
	Path  string `json:"path"`
	Focus string `json:"focus,omitempty"`
}

// FindDocumentKeyParams is the v3.3 `find_document_key` call.
// Substring match on `key` against the documents table; optional
// `kind` narrows to a document kind (i18n_json / package_json_deps /
// go_mod_requires) and `project` scopes by workspace project.
type FindDocumentKeyParams struct {
	Key     string `json:"key"`
	Kind    string `json:"kind,omitempty"`
	Project string `json:"project,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}
