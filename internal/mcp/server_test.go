package mcp

import (
	"encoding/json"
	"testing"

	"github.com/jdwiederstein/mycelium/internal/ipc"
	"github.com/jdwiederstein/mycelium/pkg/mcpschema"
)

// TestMapToolToIPC_AllSchemaToolsHaveDispatch is the protective
// contract: every tool advertised via mcpschema.Tools() must be
// routable through mapToolToIPC. A new tool added to the schema
// without a matching case here would silently surface "unknown tool"
// at runtime — caught here at compile/test time instead.
func TestMapToolToIPC_AllSchemaToolsHaveDispatch(t *testing.T) {
	for _, tool := range mcpschema.Tools() {
		// Pass empty params; most tool-call dispatch errors out before
		// unmarshalling fires. The contract we test is "the case
		// exists" — concrete unmarshal validation happens per-tool
		// during real calls.
		method, _, err := mapToolToIPC(tool.Name, json.RawMessage("{}"))
		if err != nil {
			t.Errorf("tool %q: unexpected dispatch error: %v", tool.Name, err)
			continue
		}
		if method == "" {
			t.Errorf("tool %q: empty method returned", tool.Name)
		}
	}
}

// TestMapToolToIPC_FindDocumentKey pins the v3.3 wiring specifically.
func TestMapToolToIPC_FindDocumentKey(t *testing.T) {
	raw := json.RawMessage(`{"key":"topbar.nav","kind":"i18n_json","limit":5}`)
	method, params, err := mapToolToIPC("find_document_key", raw)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if method != ipc.MethodFindDocumentKey {
		t.Errorf("method: got %q, want %q", method, ipc.MethodFindDocumentKey)
	}
	p, ok := params.(ipc.FindDocumentKeyParams)
	if !ok {
		t.Fatalf("params: wrong type %T", params)
	}
	if p.Key != "topbar.nav" || p.Kind != "i18n_json" || p.Limit != 5 {
		t.Errorf("params decoded wrong: %+v", p)
	}
}
