package query

import "github.com/datata1/mycelium/internal/ipc"

// The wire DTOs moved to internal/ipc (plans/refac/03), which owns both
// halves of the transport contract. These aliases keep the Reader API and
// every existing import site compiling unchanged.
type (
	SymbolHit           = ipc.SymbolHit
	FindSymbolResult    = ipc.FindSymbolResult
	ReferenceHit        = ipc.ReferenceHit
	GetReferencesResult = ipc.GetReferencesResult
	SearchLexicalResult = ipc.SearchLexicalResult
	FileHit             = ipc.FileHit
	FileOutlineItem     = ipc.FileOutlineItem
	Stats               = ipc.Stats
	ProjectStats        = ipc.ProjectStats
	NeighborEdge        = ipc.NeighborEdge
	NeighborNode        = ipc.NeighborNode
	Neighborhood        = ipc.Neighborhood
	ImpactHit           = ipc.ImpactHit
	Impact              = ipc.Impact
	PathVertex          = ipc.PathVertex
	CriticalPathResult  = ipc.CriticalPathResult
	FocusedRead         = ipc.FocusedRead
	FocusedReadStats    = ipc.FocusedReadStats
	FocusedSymbol       = ipc.FocusedSymbol
	FileSummary         = ipc.FileSummary
	ExportEntry         = ipc.ExportEntry
	LexicalHit          = ipc.LexicalHit
	DocumentHit         = ipc.DocumentHit
)
