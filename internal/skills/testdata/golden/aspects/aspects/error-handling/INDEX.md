---
name: error-handling
description: Symbols whose Go signature returns error.
level: aspect
heuristic: false
matches: 1
limit: 100
generated: 2026-04-26T12:00:00Z
---

# aspect: error-handling

Symbols whose Go signature returns error.

| Symbol | Package | Inbound | Signature |
|--------|---------|---------|-----------|
| `query.Reader.FindSymbol` | internal/query | 12 | `func (r *Reader) FindSymbol(ctx context.Context, name string) ([]SymbolHit, error)` |
