package wizard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// MCPClient describes a detected MCP-capable agent client.
type MCPClient struct {
	Name       string
	ConfigPath string // absolute path to the client's config file
	Detected   bool   // config file already exists on disk
}

// DetectMCPClients probes the filesystem for known agent client config
// files on Linux and macOS. Windows is explicitly out of scope.
func DetectMCPClients() []MCPClient {
	home := HomeDir()
	clients := []MCPClient{
		claudeCodeClient(home),
		cursorClient(home),
	}
	return clients
}

func claudeCodeClient(home string) MCPClient {
	path := filepath.Join(home, ".claude.json")
	_, err := os.Stat(path)
	return MCPClient{
		Name:       "Claude Code",
		ConfigPath: path,
		Detected:   err == nil,
	}
}

func cursorClient(home string) MCPClient {
	var path string
	switch runtime.GOOS {
	case "darwin":
		path = filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "cursor.mcp", "mcp.json")
	default: // linux
		path = filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "cursor.mcp", "mcp.json")
	}
	_, err := os.Stat(path)
	return MCPClient{
		Name:       "Cursor",
		ConfigPath: path,
		Detected:   err == nil,
	}
}

// WriteClaudeCodeMCP reads the existing ~/.claude.json (or starts from
// scratch), merges the mycelium MCP server entry, and writes it back.
// It preserves all existing keys and mcpServers entries. If the
// mycelium entry already exists it is updated (idempotent).
func WriteClaudeCodeMCP(configPath, binary, repoRoot string) error {
	raw := map[string]any{}
	if b, err := os.ReadFile(configPath); err == nil {
		_ = json.Unmarshal(b, &raw)
	}

	servers, _ := raw["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["mycelium"] = map[string]any{
		"command": binary,
		"args":    []string{"mcp"},
		"cwd":     repoRoot,
	}
	raw["mcpServers"] = servers

	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(configPath, append(b, '\n'), 0o644)
}

// MCPSnippet returns the JSON block the user should paste into their
// agent client config. Used as a fallback when auto-write is declined.
func MCPSnippet(binary, repoRoot string) string {
	b, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"mycelium": map[string]any{
				"command": binary,
				"args":    []string{"mcp"},
				"cwd":     repoRoot,
			},
		},
	}, "", "  ")
	return string(b)
}
