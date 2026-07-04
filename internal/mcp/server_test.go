package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/datata1/mycelium/pkg/mcpschema"
)

// runOnce feeds one JSON-RPC line through the server and decodes the
// single response. Client stays nil — initialize/tools/list never touch
// the daemon.
func runOnce(t *testing.T, request string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	s := &Server{In: strings.NewReader(request + "\n"), Out: &out, Version: "test"}
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	var resp struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", out.String(), err)
	}
	return resp.Result
}

func TestInitialize_VersionNegotiation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		client string // "" = no protocolVersion param
		want   string
	}{
		{"echoes_supported_old", "2024-11-05", "2024-11-05"},
		{"echoes_supported_current", "2025-03-26", "2025-03-26"},
		{"echoes_supported_next", "2025-06-18", "2025-06-18"},
		{"unknown_gets_preferred", "2099-01-01", mcpschema.ProtocolVersion},
		{"absent_gets_preferred", "", mcpschema.ProtocolVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := "{}"
			if tc.client != "" {
				params = `{"protocolVersion":"` + tc.client + `"}`
			}
			res := runOnce(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+params+`}`)
			if got := res["protocolVersion"]; got != tc.want {
				t.Errorf("protocolVersion = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestToolsList_CarriesAnnotations(t *testing.T) {
	t.Parallel()
	res := runOnce(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools, ok := res["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list returned no tools: %v", res)
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name, _ := tool["name"].(string)
		ann, ok := tool["annotations"].(map[string]any)
		if !ok {
			t.Errorf("%s: missing annotations", name)
			continue
		}
		if ann["readOnlyHint"] != true {
			t.Errorf("%s: readOnlyHint = %v, want true", name, ann["readOnlyHint"])
		}
		if title, _ := ann["title"].(string); title == "" {
			t.Errorf("%s: missing human title", name)
		}
		if ann["openWorldHint"] != false {
			t.Errorf("%s: openWorldHint = %v, want false", name, ann["openWorldHint"])
		}
	}
}
