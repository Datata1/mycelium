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
	MethodFindSymbol     = "find_symbol"
	MethodGetReferences  = "get_references"
	MethodListFiles      = "list_files"
	MethodGetFileOutline = "get_file_outline"
	MethodStats          = "stats"
	MethodReindex        = "reindex"
	MethodSearchSemantic = "search_semantic"
	MethodPing           = "ping"
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

type FindSymbolParams struct {
	Name  string `json:"name"`
	Kind  string `json:"kind,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type GetReferencesParams struct {
	Target string `json:"target"`
	Limit  int    `json:"limit,omitempty"`
}

type ListFilesParams struct {
	Language     string `json:"language,omitempty"`
	NameContains string `json:"name_contains,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type GetFileOutlineParams struct {
	Path string `json:"path"`
}

type SearchSemanticParams struct {
	Query        string `json:"query"`
	K            int    `json:"k,omitempty"`
	Kind         string `json:"kind,omitempty"`
	PathContains string `json:"path_contains,omitempty"`
}
