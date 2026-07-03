// Package registry is the single tool table: it binds each read-surface
// ipc method to its Service handler and its renderer. The daemon
// dispatcher, the MCP server, and the CLI fallback all route through it,
// so adding a tool is one entry here (plus its mcpschema schema, ipc
// types, Service method, and render func — the parity test enumerates
// anything forgotten).
package registry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/mcp/render"
	"github.com/datata1/mycelium/internal/service"
)

// Handler executes one tool call from raw wire params.
type Handler func(ctx context.Context, svc *service.Service, raw json.RawMessage) (any, error)

// Tool is one row of the table.
type Tool struct {
	Method ipc.Method
	Handle Handler
	Render func(json.RawMessage) string
}

// bind adapts a typed Service method to Handler. The compiler checks per
// entry that P matches the method's parameter type and R its result.
func bind[P, R any](fn func(*service.Service, context.Context, P) (R, error)) Handler {
	return func(ctx context.Context, svc *service.Service, raw json.RawMessage) (any, error) {
		var p P
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("%w: %v", ipc.ErrBadParams, err)
			}
		}
		return fn(svc, ctx, p)
	}
}

// stats takes no params; the wrapper ignores whatever the client sent.
func statsHandler(svc *service.Service, ctx context.Context, _ struct{}) (ipc.Stats, error) {
	return svc.Stats(ctx)
}

var tools = []Tool{
	{ipc.MethodFindSymbol, bind((*service.Service).FindSymbol), render.FindSymbol},
	{ipc.MethodGetReferences, bind((*service.Service).GetReferences), render.References},
	{ipc.MethodListFiles, bind((*service.Service).ListFiles), render.ListFiles},
	{ipc.MethodGetFileOutline, bind((*service.Service).GetFileOutline), render.FileOutline},
	{ipc.MethodGetFileSummary, bind((*service.Service).GetFileSummary), render.FileSummary},
	{ipc.MethodGetNeighborhood, bind((*service.Service).GetNeighborhood), render.Neighborhood},
	{ipc.MethodSearchLexical, bind((*service.Service).SearchLexical), render.Lexical},
	{ipc.MethodStats, bind(statsHandler), render.Stats},
	{ipc.MethodImpactAnalysis, bind((*service.Service).ImpactAnalysis), render.Impact},
	{ipc.MethodCriticalPath, bind((*service.Service).CriticalPath), render.CriticalPath},
	{ipc.MethodReadFocused, bind((*service.Service).ReadFocused), render.RawJSON},
	{ipc.MethodFindDocumentKey, bind((*service.Service).FindDocumentKey), render.DocumentKey},
}

var byMethod = func() map[ipc.Method]Tool {
	m := make(map[ipc.Method]Tool, len(tools))
	for _, t := range tools {
		m[t.Method] = t
	}
	return m
}()

// Lookup returns the tool for m.
func Lookup(m ipc.Method) (Tool, bool) {
	t, ok := byMethod[m]
	return t, ok
}

// Methods returns the registered methods in table order.
func Methods() []ipc.Method {
	out := make([]ipc.Method, len(tools))
	for i, t := range tools {
		out[i] = t.Method
	}
	return out
}

// Render formats a result with the tool's renderer; unknown methods fall
// back to raw JSON.
func Render(m ipc.Method, raw json.RawMessage) string {
	if t, ok := Lookup(m); ok {
		return t.Render(raw)
	}
	return render.RawJSON(raw)
}
