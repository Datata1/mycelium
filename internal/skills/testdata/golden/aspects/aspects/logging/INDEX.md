---
name: logging
description: Symbols that call into stdlib log.* or a *Logger interface.
level: aspect
heuristic: true
matches: 1
limit: 100
generated: 2026-04-26T12:00:00Z
---

# aspect: logging

Symbols that call into stdlib log.* or a *Logger interface.

_Heuristic filter — false positives possible. Use `myco query refs <symbol>` to verify._

| Symbol | Package | Inbound | Signature |
|--------|---------|---------|-----------|
| `daemon.stderrLogger.Printf` | internal/query | 1 | `func (stderrLogger) Printf(format string, args ...any)` |
