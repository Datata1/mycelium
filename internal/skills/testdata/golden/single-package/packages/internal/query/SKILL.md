---
name: internal/query
description: Package query (2 files, 5 top-level symbols).
level: package
language: go
files: 2
symbols: 7
top_level_symbols: 5
generated: 2026-04-26T12:00:00Z
---

# internal/query

## Top-level symbols (5)

### Function
- `NewReader` — internal/query/query.go:19
  - signature: `func NewReader(db *sql.DB) *Reader`
- `FindSymbol` — internal/query/query.go:45
  - signature: `func (r *Reader) FindSymbol(ctx context.Context, name string) ([]SymbolHit, error)`
- `GetFileSummary` — internal/query/summary.go:34
  - signature: `func (r *Reader) GetFileSummary(ctx context.Context, p string) (FileSummary, error)`

### Type
- `FileSummary` — internal/query/summary.go:8
  - signature: `type FileSummary struct{ Path string }`
- `Reader` — internal/query/query.go:15
  - signature: `type Reader struct{ db *sql.DB }`

## Top inbound (callers of this package)
- internal/daemon — 78 refs
- cmd/myco — 22 refs

## Top outbound (packages this package calls into)
- internal/index — 12 refs

## See also
- For specific reference sites: `myco query refs <symbol>`
- For neighborhood exploration: `myco query neighbors <symbol>`
