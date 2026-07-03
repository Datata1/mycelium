package ipc

import "time"

// This file owns the result half of the wire contract: the JSON shapes
// every transport (socket, HTTP, MCP) returns. They moved verbatim from
// internal/query (plans/refac/03) — internal/query keeps type aliases so
// its Reader API is unchanged. JSON tags here ARE the compatibility
// contract; changing them changes what agents see on the wire.

// SymbolHit is the canonical shape returned by symbol-producing queries.
//
// `Project` (v3.1.2+) carries the workspace project name when the file
// belongs to a configured project, or "" in single-project mode / for
// root-level files. Agents can use it to disambiguate paths when the
// same path exists in multiple workspace projects (e.g. `src/index.ts`
// across several packages).
type SymbolHit struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Project   string `json:"project,omitempty"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Signature string `json:"signature,omitempty"`
	Docstring string `json:"docstring,omitempty"`
}

// FindSymbolResult wraps the matches plus optional diagnostic hints
// (v3.1). Hints populate when Matches is empty *and* the diagnose path
// has something useful to say about why — typo'd project name,
// kind filter that eliminated all real matches, etc. Empty Hints on an
// empty Matches means "the index just doesn't contain that symbol."
//
// Always returned with non-nil Matches (empty slice rather than nil) so
// JSON serialisation produces `[]` instead of `null` — agents
// distinguish "tool returned nothing" from "tool errored" more reliably
// when the shape is consistent.
type FindSymbolResult struct {
	Matches []SymbolHit `json:"matches"`
	Hints   []string    `json:"hints,omitempty"`
}

// ReferenceHit is one call/import/type-use site pointing at a symbol.
// Resolved=true means dst_symbol_id is populated; otherwise the hit is
// a textual-only match on dst_name.
//
// `SrcProject` (v3.1.2+) is the workspace project of the source file,
// or "" when the file has no configured project.
type ReferenceHit struct {
	ID            int64  `json:"id"`
	SrcPath       string `json:"src_path"`
	SrcProject    string `json:"src_project,omitempty"`
	SrcLine       int    `json:"src_line"`
	SrcCol        int    `json:"src_col"`
	SrcSymbolID   int64  `json:"src_symbol_id,omitempty"`
	SrcSymbolName string `json:"src_symbol_name,omitempty"`
	DstName       string `json:"dst_name"`
	DstSymbolID   int64  `json:"dst_symbol_id,omitempty"`
	Kind          string `json:"kind"`
	Resolved      bool   `json:"resolved"`
}

// FileHit represents a file in the index.
//
// `Project` (v3.1.2+) is the workspace project the file belongs to,
// or "" in single-project mode.
type FileHit struct {
	Path        string    `json:"path"`
	Project     string    `json:"project,omitempty"`
	Language    string    `json:"language"`
	SymbolCount int       `json:"symbol_count"`
	SizeBytes   int64     `json:"size_bytes"`
	LastIndexed time.Time `json:"last_indexed"`
}

// FileOutlineItem is one entry in the hierarchical outline of a file.
type FileOutlineItem struct {
	SymbolID  int64             `json:"symbol_id"`
	Name      string            `json:"name"`
	Qualified string            `json:"qualified"`
	Kind      string            `json:"kind"`
	StartLine int               `json:"start_line"`
	EndLine   int               `json:"end_line"`
	Signature string            `json:"signature,omitempty"`
	Children  []FileOutlineItem `json:"children,omitempty"`
}

// Stats exposes point-in-time counts and quality signals for the index.
// Quality signals (self_loop_count, unresolved_by_language, stale_chunks)
// were added in v1.1 so `myco doctor` and agents have honest numbers to
// judge the index by before running expensive tools.
type Stats struct {
	Files    int `json:"files"`
	Symbols  int `json:"symbols"`
	Refs     int `json:"refs"`
	Resolved int `json:"refs_resolved"`
	// v1.2 refs breakdown — captures the three honest states.
	// Invariant: Resolved + RefsExternalKnown + RefsTrulyUnresolved + <v0 short-name resolves>
	// = NonImportRefs. Import refs are counted separately.
	NonImportRefs        int            `json:"non_import_refs"`       // kind != 'import'
	RefsTypeResolved     int            `json:"refs_type_resolved"`    // resolver_version >= 1
	RefsExternalKnown    int            `json:"refs_external_known"`   // v1 + dst NULL (stdlib / deps)
	RefsTrulyUnresolved  int            `json:"refs_truly_unresolved"` // v0 + dst NULL + kind != import
	SelfLoopCount        int            `json:"self_loop_count"`       // v0 self-loops (resolution bugs)
	RecursionSelfLoops   int            `json:"recursion_self_loops"`  // v>=1 self-loops (real recursion)
	UnresolvedByLanguage map[string]int `json:"unresolved_by_language"`
	TotalByLanguage      map[string]int `json:"total_refs_by_language"`
	ByKind               map[string]int `json:"by_kind"`
	ByLang               map[string]int `json:"by_language"`
	DBSizeBytes          int64          `json:"db_size_bytes"`
	DBFreelistPages      int            `json:"db_freelist_pages"`
	DBPageCount          int            `json:"db_page_count"`
	// v2.1: interface-implementer linkage signal. Counts RefInherit edges
	// emitted by language resolvers and the distinct concrete types they
	// originate from. Surfaces via doctor as `interface_expansion_coverage`
	// so users can confirm the fan-out is actually populated.
	InterfaceImplementsRefs int `json:"interface_implements_refs"`
	InterfaceConcreteTypes  int `json:"interface_concrete_types"`
	// v3.3: per-kind document entry counts. Empty when no document
	// parsers are wired up or the repo has no matching files.
	// Doctor surfaces this as `documents_indexed`; a registered
	// kind with no entries paired with at least one candidate file
	// on disk is the diagnostic shape worth flagging.
	DocumentsByKind map[string]int `json:"documents_by_kind,omitempty"`
	// v3.1: configured workspace projects with per-project file counts.
	// Empty when no `projects:` block is set in `.mycelium.yml` (the
	// repo runs in single-project mode and `files.project_id` is NULL
	// everywhere). Used by doctor to flag misconfigured projects whose
	// include patterns matched nothing, and by FindSymbol's hint
	// generator to tell agents which project names are valid.
	ConfiguredProjects []ProjectStats `json:"configured_projects,omitempty"`
	LastScan           time.Time      `json:"last_scan"`
}

// UnresolvedRatio is the fraction of *non-import* refs that no resolver
// could place — neither the type-aware pass nor the short-name fallback.
// Type-resolved external refs (e.g. fmt.Println) don't count as unresolved
// even though their dst_symbol_id is NULL: we know exactly what they are,
// we just don't index the target package.
//
// Agents use this to decide whether to trust graph-traversal results.
// Lower is better; v1.2 target is <8% for Go.
func (s Stats) UnresolvedRatio() float64 {
	if s.NonImportRefs == 0 {
		return 0
	}
	return float64(s.RefsTrulyUnresolved) / float64(s.NonImportRefs)
}

// DBFragmentation is freelist_pages / page_count; a rough VACUUM signal.
func (s Stats) DBFragmentation() float64 {
	if s.DBPageCount == 0 {
		return 0
	}
	return float64(s.DBFreelistPages) / float64(s.DBPageCount)
}

// ProjectStats is the per-project file count surfaced via Stats.
type ProjectStats struct {
	Name      string `json:"name"`
	Root      string `json:"root"`
	FileCount int    `json:"file_count"`
}

// NeighborEdge is one edge in the returned graph.
//
// `SrcProject` (v3.1.2+) is the workspace project of the source-file
// edge location, or "" in single-project mode.
type NeighborEdge struct {
	FromID     int64  `json:"from_id"`
	FromName   string `json:"from_name"`
	ToID       int64  `json:"to_id"`
	ToName     string `json:"to_name"`
	Kind       string `json:"kind"` // call | import | type_ref | inherit
	SrcPath    string `json:"src_path,omitempty"`
	SrcProject string `json:"src_project,omitempty"`
	SrcLine    int    `json:"src_line,omitempty"`
	Depth      int    `json:"depth"`
	Direction  string `json:"direction"` // "out" or "in"
}

// NeighborNode is one vertex in the returned graph.
//
// `Project` (v3.1.2+) is the workspace project the symbol's file
// belongs to, or "" in single-project mode.
type NeighborNode struct {
	ID        int64  `json:"id"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Project   string `json:"project,omitempty"`
	StartLine int    `json:"start_line"`
	Depth     int    `json:"depth"` // 0 = seed, 1 = direct neighbor, ...
}

// Neighborhood is the result of a traversal.
//
// Notes carries non-fatal messages the caller should surface (e.g. depth
// was silently clamped). Transports (CLI, MCP, HTTP) pass these through
// verbatim so agents can reason about the result quality.
type Neighborhood struct {
	Seed  NeighborNode   `json:"seed"`
	Nodes []NeighborNode `json:"nodes"`
	Edges []NeighborEdge `json:"edges"`
	Notes []string       `json:"notes,omitempty"`
}

// ImpactHit is one transitive caller of the seed symbol, ranked by
// distance. Lower distance = closer to the seed (distance 1 = direct
// caller).
//
// `Project` (v3.1.2+) is the workspace project the caller's file
// belongs to, or "" in single-project mode.
type ImpactHit struct {
	ID        int64  `json:"id"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Project   string `json:"project,omitempty"`
	StartLine int    `json:"start_line"`
	Distance  int    `json:"distance"`
}

// Impact is the result of ImpactAnalysis. Notes mirrors the
// Neighborhood convention for surfacing depth-clamp messages.
type Impact struct {
	Seed  NeighborNode `json:"seed"`
	Hits  []ImpactHit  `json:"hits"`
	Notes []string     `json:"notes,omitempty"`
}

// PathVertex is one step in a CriticalPath result.
//
// `Project` (v3.1.2+) is the workspace project the vertex's file
// belongs to, or "" in single-project mode.
type PathVertex struct {
	ID        int64  `json:"id"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Project   string `json:"project,omitempty"`
	StartLine int    `json:"start_line"`
}

// CriticalPathResult carries one or more shortest outbound paths from
// the `from` seed to the `to` target. Empty Paths means no route was
// found within the depth cap.
type CriticalPathResult struct {
	From  NeighborNode   `json:"from"`
	To    NeighborNode   `json:"to"`
	Paths [][]PathVertex `json:"paths"`
	Notes []string       `json:"notes,omitempty"`
}

// FocusedRead is the result of ReadFocused: a single file's content
// rendered with focus-matched symbols expanded in full and non-matched
// symbols collapsed to a one-line marker. Stats let agents reason about
// how much was hidden and which line ranges to drill into next.
//
// `Hint` is populated only in the v4 no-focus preview path (focus=""):
// it tells the agent that Content is a truncated preview and how to get
// more (pass focus, or call get_file_outline). When focus is set, Hint
// is empty.
type FocusedRead struct {
	Path    string           `json:"path"`
	Focus   string           `json:"focus"`
	Content string           `json:"content"`
	Stats   FocusedReadStats `json:"stats"`
	Hint    string           `json:"hint,omitempty"`
	// Expanded reports each symbol that survived the filter, with its
	// original line range, so agents can map back to source for follow-ups.
	Expanded []FocusedSymbol `json:"expanded,omitempty"`
}

// FocusedReadStats summarises the collapse outcome.
type FocusedReadStats struct {
	TotalSymbols    int `json:"total_symbols"`
	ExpandedSymbols int `json:"expanded_symbols"`
	OriginalBytes   int `json:"original_bytes"`
	ReturnedBytes   int `json:"returned_bytes"`
}

// FocusedSymbol is a kept-after-filter symbol's location.
type FocusedSymbol struct {
	Qualified string  `json:"qualified"`
	Kind      string  `json:"kind"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
}

// FileSummary is a structural summary of a single file. For v1.0 we stay
// firmly on the "derivable from the index" side — no LLM calls. Agents use
// this as a quick orientation before deciding whether to read the file.
//
// `Project` (v3.1.2+) is the workspace project of the file, or "".
type FileSummary struct {
	Path        string         `json:"path"`
	Project     string         `json:"project,omitempty"`
	Language    string         `json:"language"`
	LOC         int            `json:"loc"`
	SymbolCount int            `json:"symbol_count"`
	ByKind      map[string]int `json:"by_kind"`
	Exports     []ExportEntry  `json:"exports"`
	Imports     []string       `json:"imports"`
}

// ExportEntry is one publicly-visible symbol. We filter on visibility=public
// which covers Go capital-names, TS export_statement-wrapped defs, and Python
// non-underscore names.
type ExportEntry struct {
	Name      string `json:"name"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"`
	StartLine int    `json:"start_line"`
	Signature string `json:"signature,omitempty"`
}

// LexicalHit is one matching line in a file.
//
// `Project` (v3.1.2+) is the workspace project the file belongs to, or
// "" in single-project mode.
type LexicalHit struct {
	Path    string `json:"path"`
	Project string `json:"project,omitempty"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

// DocumentHit is one (key, value, line) entry from the v3.3 documents
// surface. Project annotates the workspace project the entry's file
// belongs to ("" in single-project mode); Path follows the v3.1.2
// convention — pass it verbatim to `read_focused` / `get_file_outline`
// without prepending project roots.
type DocumentHit struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Project string `json:"project,omitempty"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	Line    int    `json:"line"`
}
