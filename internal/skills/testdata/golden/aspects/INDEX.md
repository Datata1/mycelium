---
name: mycelium-skills
description: Browseable directory of every package's SKILL.md.
level: index
package_count: 1
file_count: 1
symbol_count: 1
generated: 2026-04-26T12:00:00Z
---

# Mycelium skills tree

Each row links to a per-package SKILL.md under `packages/`.
Read the SKILL.md for an overview; use the MCP tools (or
`myco query …`) for specific reference / impact / search queries.

## Packages

| Package | Language | Files | Symbols |
|---------|----------|-------|---------|
| [internal/query](packages/internal/query/SKILL.md) | go | 1 | 1 |

## Aspects (cross-cutting)

| Aspect | Matches | Heuristic? | Description |
|--------|---------|------------|-------------|
| [error-handling](aspects/error-handling/INDEX.md) | 1 | no | Symbols whose Go signature returns error. |
| [context-propagation](aspects/context-propagation/INDEX.md) | 1 | no | Symbols that take or return context.Context (Go). |
| [config-loading](aspects/config-loading/INDEX.md) | 1 | yes | Symbols with at least one outbound ref into internal/config. |
| [logging](aspects/logging/INDEX.md) | 1 | yes | Symbols that call into stdlib log.* or a *Logger interface. |
