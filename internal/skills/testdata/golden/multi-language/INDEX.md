---
name: mycelium-skills
description: Browseable directory of every package's SKILL.md.
level: index
package_count: 1
file_count: 3
symbol_count: 3
generated: 2026-04-26T12:00:00Z
---

# Mycelium skills tree

Each row links to a per-package SKILL.md under `packages/`.
Read the SKILL.md for an overview; use the MCP tools (or
`myco query …`) for specific reference / impact / search queries.

## Packages

| Package | Language | Files | Symbols |
|---------|----------|-------|---------|
| [src](packages/src/SKILL.md) | mixed | 3 | 3 |

## Aspects (cross-cutting)

| Aspect | Matches | Heuristic? | Description |
|--------|---------|------------|-------------|
| [error-handling](aspects/error-handling/INDEX.md) | 0 | no | Symbols whose Go signature returns error. |
| [context-propagation](aspects/context-propagation/INDEX.md) | 0 | no | Symbols that take or return context.Context (Go). |
| [config-loading](aspects/config-loading/INDEX.md) | 0 | yes | Symbols with at least one outbound ref into internal/config. |
| [logging](aspects/logging/INDEX.md) | 0 | yes | Symbols that call into stdlib log.* or a *Logger interface. |
