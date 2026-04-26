---
name: context-propagation
description: Symbols that take or return context.Context (Go).
level: aspect
heuristic: false
matches: 1
limit: 100
generated: 2026-04-26T12:00:00Z
---

# aspect: context-propagation

Symbols that take or return context.Context (Go).

| Symbol | Package | Inbound | Signature |
|--------|---------|---------|-----------|
| `query.Reader.FindSymbol` | internal/query | 12 | `func (r *Reader) FindSymbol(ctx context.Context, name string) ([]SymbolHit, error)` |
