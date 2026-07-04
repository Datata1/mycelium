# WS06 — MCP Tool Annotations & Protocol Bump

Size: **S/M**. Depends on: nothing. Ships independently.

## Problem

`mcpschema.Tool` carries only `Name`, `Description`, `InputSchema`
(`pkg/mcpschema/tools.go:13-17`); the server advertises
`protocolVersion: "2024-11-05"` (`internal/mcp/server.go:66`), which
predates tool annotations (added 2025-03-26). All 12 tools are read-only,
but clients can't know that — `readOnlyHint` affects permission prompts and
auto-approval eligibility, which is client-side friction that suppresses
tool usage. Titles make the tools legible in client tool lists.

## Mechanism

1. Extend the schema types:

   ```go
   type ToolAnnotations struct {
       Title          string `json:"title,omitempty"`
       ReadOnlyHint   bool   `json:"readOnlyHint"`
       IdempotentHint bool   `json:"idempotentHint,omitempty"`
       OpenWorldHint  *bool  `json:"openWorldHint,omitempty"`
   }
   type Tool struct {
       Name        string          `json:"name"`
       Description string          `json:"description"`
       InputSchema map[string]any  `json:"inputSchema"`
       Annotations *ToolAnnotations `json:"annotations,omitempty"`
   }
   ```

   All 12 tools get `ReadOnlyHint: true`, `OpenWorldHint: false` (local
   index only), and a human title ("Find symbol definition", "List callers
   of a symbol", "Read file with focus filter", ...).

2. Version negotiation (`internal/mcp/server.go:60-66`): parse
   `params.protocolVersion` from `initialize`. Supported set
   `{"2024-11-05", "2025-03-26", "2025-06-18"}`: echo the client's version
   when in-set, otherwise respond with the preferred `2025-03-26` (per
   spec, the server answers with the version it wants; the client
   disconnects if incompatible). Nothing else in the spec delta is
   mandatory for a tools-only stdio server (JSON-RPC batching removal in
   2025-06-18 is irrelevant — this server never batched; the
   `MCP-Protocol-Version` header is HTTP-transport-only).

Explicitly rejected: `structuredContent` dual output (2025-06-18) — doubles
token cost for zero adoption gain.

## Risks

Low. Annotations are additive JSON — older clients ignore unknown fields.
Negotiation is ~15 lines.

## Tests

- Table-driven `initialize` round-trips (client version in-set / unknown /
  absent) in the existing mcp server test file.
- One assertion that `tools/list` carries annotations on every tool.
