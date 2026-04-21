package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jdwiederstein/mycelium/internal/ipc"
	"github.com/jdwiederstein/mycelium/pkg/mcpschema"
)

// Server implements a minimal Model Context Protocol server over stdio.
// It is a thin bridge: every tool call translates into a single ipc.Client
// call against the running daemon. This keeps the MCP process cheap and
// short-lived (Claude Code spawns one per session).
type Server struct {
	In     io.Reader
	Out    io.Writer
	Client *ipc.Client
}

// Run reads newline-delimited JSON-RPC from In, dispatches each request, and
// writes the response to Out. It returns when In is closed (EOF) or ctx is
// cancelled.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.In)
	// MCP messages can be larger than the default 64KB buffer — bump it.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(s.Out)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req jsonrpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeError(enc, nil, -32700, fmt.Sprintf("parse error: %v", err))
			continue
		}
		s.handle(ctx, enc, req)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("mcp scan: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, enc *json.Encoder, req jsonrpcRequest) {
	// Notifications have no id and expect no response.
	notification := req.ID == nil
	switch req.Method {
	case "initialize":
		writeResult(enc, req.ID, map[string]any{
			"protocolVersion": mcpschema.ProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    mcpschema.ServerName,
				"version": mcpschema.ServerVersion,
			},
		})
	case "initialized", "notifications/initialized":
		// Client confirms initialization. No response required.
		return
	case "tools/list":
		writeResult(enc, req.ID, map[string]any{
			"tools": mcpschema.Tools(),
		})
	case "tools/call":
		s.handleToolCall(ctx, enc, req)
	case "ping":
		writeResult(enc, req.ID, map[string]any{})
	case "shutdown":
		writeResult(enc, req.ID, nil)
	default:
		if notification {
			return
		}
		writeError(enc, req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func (s *Server) handleToolCall(ctx context.Context, enc *json.Encoder, req jsonrpcRequest) {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(enc, req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		return
	}
	method, ipcParams, err := mapToolToIPC(params.Name, params.Arguments)
	if err != nil {
		writeError(enc, req.ID, -32602, err.Error())
		return
	}
	var result json.RawMessage
	if err := s.Client.Call(method, ipcParams, &result); err != nil {
		// MCP convention: tool errors are returned as a successful response
		// with isError=true, so the model can reason about the failure.
		writeResult(enc, req.ID, map[string]any{
			"isError": true,
			"content": []map[string]any{
				{"type": "text", "text": err.Error()},
			},
		})
		return
	}
	writeResult(enc, req.ID, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(result)},
		},
	})
	_ = ctx
}

// mapToolToIPC translates an MCP tool call into the matching daemon RPC.
// The param shapes are identical by design, so this is mostly a name map.
func mapToolToIPC(tool string, rawArgs json.RawMessage) (string, any, error) {
	switch tool {
	case "find_symbol":
		var p ipc.FindSymbolParams
		if err := unmarshalArgs(rawArgs, &p); err != nil {
			return "", nil, err
		}
		return ipc.MethodFindSymbol, p, nil
	case "get_references":
		var p ipc.GetReferencesParams
		if err := unmarshalArgs(rawArgs, &p); err != nil {
			return "", nil, err
		}
		return ipc.MethodGetReferences, p, nil
	case "list_files":
		var p ipc.ListFilesParams
		if err := unmarshalArgs(rawArgs, &p); err != nil {
			return "", nil, err
		}
		return ipc.MethodListFiles, p, nil
	case "get_file_outline":
		var p ipc.GetFileOutlineParams
		if err := unmarshalArgs(rawArgs, &p); err != nil {
			return "", nil, err
		}
		return ipc.MethodGetFileOutline, p, nil
	case "stats":
		return ipc.MethodStats, nil, nil
	default:
		return "", nil, fmt.Errorf("unknown tool: %s", tool)
	}
}

func unmarshalArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// --- JSON-RPC 2.0 envelope --------------------------------------------------

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any            `json:"result,omitempty"`
	Error   *jsonrpcError  `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func writeResult(enc *json.Encoder, id json.RawMessage, result any) {
	_ = enc.Encode(jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeError(enc *json.Encoder, id json.RawMessage, code int, msg string) {
	_ = enc.Encode(jsonrpcResponse{JSONRPC: "2.0", ID: id, Error: &jsonrpcError{Code: code, Message: msg}})
}
