package registry

import (
	"testing"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/pkg/mcpschema"
)

// TestToolParity asserts the three per-tool lists stay in lockstep:
// the public MCP schemas, the ipc method enumeration, and this table.
// Adding a tool and forgetting one site fails here with its name.
func TestToolParity(t *testing.T) {
	registered := map[string]bool{}
	for _, m := range Methods() {
		registered[string(m)] = true
	}

	schema := map[string]bool{}
	for _, tool := range mcpschema.Tools() {
		schema[tool.Name] = true
	}

	wire := map[string]bool{}
	for _, m := range ipc.AllMethods {
		wire[string(m)] = true
	}

	for name := range schema {
		if !registered[name] {
			t.Errorf("mcpschema tool %q missing from registry table", name)
		}
		if !wire[name] {
			t.Errorf("mcpschema tool %q missing from ipc.AllMethods", name)
		}
	}
	for name := range registered {
		if !schema[name] {
			t.Errorf("registry tool %q has no mcpschema entry", name)
		}
	}
	for name := range wire {
		if !registered[name] {
			t.Errorf("ipc.AllMethods entry %q missing from registry table", name)
		}
	}

	for _, tool := range tools {
		if tool.Handle == nil {
			t.Errorf("tool %q has nil Handle", tool.Method)
		}
		if tool.Render == nil {
			t.Errorf("tool %q has nil Render", tool.Method)
		}
	}
}
