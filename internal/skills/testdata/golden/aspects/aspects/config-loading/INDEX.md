---
name: config-loading
description: Symbols with at least one outbound ref into internal/config.
level: aspect
heuristic: true
matches: 1
limit: 100
generated: 2026-04-26T12:00:00Z
---

# aspect: config-loading

Symbols with at least one outbound ref into internal/config.

_Heuristic filter — false positives possible. Use `myco query refs <symbol>` to verify._

| Symbol | Package | Inbound | Signature |
|--------|---------|---------|-----------|
| `config.Load` | internal/query | 4 | `func Load(path string) (Config, error)` |
