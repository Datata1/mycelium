package registry

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/query"
)

var update = flag.Bool("update", false, "rewrite golden files")

// goldenCases covers every method Render dispatches on, plus the
// unknown-method JSON fallback. Results are built from the query structs
// (not hand-written JSON) so a field rename breaks the test at compile
// time rather than silently changing the rendered output.
func goldenCases() []struct {
	name   string
	method string
	result any
} {
	seed := query.NeighborNode{
		ID:        1,
		Qualified: "AuthService.Login",
		Kind:      "method",
		Path:      "internal/auth/service.go",
		StartLine: 42,
		Depth:     0,
	}
	return []struct {
		name   string
		method string
		result any
	}{
		{
			name:   "find_symbol",
			method: "find_symbol",
			result: query.FindSymbolResult{
				Matches: []query.SymbolHit{
					{
						ID:        1,
						Name:      "Login",
						Qualified: "AuthService.Login",
						Kind:      "method",
						Path:      "internal/auth/service.go",
						StartLine: 42,
						EndLine:   78,
						Signature: "func (s *AuthService) Login(ctx context.Context, creds Credentials) (*Session, error)",
						Docstring: "Login authenticates a user and mints a session.\nSecond line is truncated by the renderer.",
					},
					{
						ID:        2,
						Name:      "Login",
						Qualified: "handlers.Login",
						Kind:      "function",
						Path:      "internal/http/handlers/login.go",
						StartLine: 15,
						EndLine:   40,
					},
				},
			},
		},
		{
			name:   "find_symbol_empty",
			method: "find_symbol",
			result: query.FindSymbolResult{
				Matches: []query.SymbolHit{},
				Hints: []string{
					`project "backned" not found; configured projects: backend, frontend`,
					`kind filter "class" excluded 3 matches`,
				},
			},
		},
		{
			name:   "get_file_outline",
			method: "get_file_outline",
			result: []query.FileOutlineItem{
				{
					SymbolID:  10,
					Name:      "AuthService",
					Qualified: "AuthService",
					Kind:      "struct",
					StartLine: 20,
					EndLine:   90,
					Children: []query.FileOutlineItem{
						{
							SymbolID:  11,
							Name:      "Login",
							Qualified: "AuthService.Login",
							Kind:      "method",
							StartLine: 42,
							EndLine:   78,
						},
						{
							SymbolID:  12,
							Name:      "Logout",
							Qualified: "AuthService.Logout",
							Kind:      "method",
							StartLine: 80,
							EndLine:   90,
						},
					},
				},
				{
					SymbolID:  13,
					Name:      "NewAuthService",
					Qualified: "NewAuthService",
					Kind:      "function",
					StartLine: 95,
					EndLine:   102,
				},
			},
		},
		{
			name:   "get_file_summary",
			method: "get_file_summary",
			result: query.FileSummary{
				Path:        "internal/auth/service.go",
				Language:    "go",
				LOC:         240,
				SymbolCount: 4,
				// Single entry only: the renderer iterates this map in
				// Go's randomized order, so multiple keys would make
				// the golden output flaky.
				ByKind: map[string]int{"method": 2},
				Exports: []query.ExportEntry{
					{
						Name:      "AuthService",
						Qualified: "AuthService",
						Kind:      "struct",
						StartLine: 20,
					},
					{
						Name:      "NewAuthService",
						Qualified: "NewAuthService",
						Kind:      "function",
						StartLine: 95,
					},
				},
				Imports: []string{"context", "errors", "internal/session"},
			},
		},
		{
			name:   "get_references",
			method: "get_references",
			result: query.GetReferencesResult{
				Matches: []query.ReferenceHit{
					{
						ID:            100,
						SrcPath:       "internal/http/handlers/login.go",
						SrcLine:       28,
						SrcCol:        9,
						SrcSymbolID:   2,
						SrcSymbolName: "handlers.Login",
						DstName:       "Login",
						DstSymbolID:   1,
						Kind:          "call",
						Resolved:      true,
					},
					{
						ID:       101,
						SrcPath:  "internal/auth/service_test.go",
						SrcLine:  55,
						SrcCol:   12,
						DstName:  "Login",
						Kind:     "call",
						Resolved: false,
					},
				},
			},
		},
		{
			name:   "get_references_empty_hints",
			method: "get_references",
			result: query.GetReferencesResult{
				Matches: []query.ReferenceHit{},
				Hints: []string{
					`no symbol or reference named "Logn" in the index — find_symbol("Logn") does substring matching and catches qualified forms; check spelling/qualification`,
				},
			},
		},
		{
			name:   "get_neighborhood",
			method: "get_neighborhood",
			result: query.Neighborhood{
				Seed: seed,
				Nodes: []query.NeighborNode{
					seed,
					{
						ID:        2,
						Qualified: "handlers.Login",
						Kind:      "function",
						Path:      "internal/http/handlers/login.go",
						StartLine: 15,
						Depth:     1,
					},
					{
						ID:        3,
						Qualified: "session.Mint",
						Kind:      "function",
						Path:      "internal/session/mint.go",
						StartLine: 30,
						Depth:     1,
					},
					{
						ID:        5,
						Qualified: "crypto.Sign",
						Kind:      "function",
						Path:      "internal/crypto/sign.go",
						StartLine: 9,
						Depth:     2,
					},
				},
				Edges: []query.NeighborEdge{
					{
						FromID:    1,
						FromName:  "AuthService.Login",
						ToID:      3,
						ToName:    "session.Mint",
						Kind:      "call",
						SrcPath:   "internal/auth/service.go",
						SrcLine:   60,
						Depth:     1,
						Direction: "out",
					},
					{
						// Second call site into the same callee: must render
						// as one row, not a duplicate.
						FromID:    1,
						FromName:  "AuthService.Login",
						ToID:      3,
						ToName:    "session.Mint",
						Kind:      "call",
						SrcPath:   "internal/auth/service.go",
						SrcLine:   71,
						Depth:     1,
						Direction: "out",
					},
					{
						// Depth-2 hop: the row keeps the near endpoint so the
						// topology (Mint -> Sign) survives the flat list.
						FromID:    3,
						FromName:  "session.Mint",
						ToID:      5,
						ToName:    "crypto.Sign",
						Kind:      "call",
						SrcPath:   "internal/session/mint.go",
						SrcLine:   41,
						Depth:     2,
						Direction: "out",
					},
					{
						FromID:    2,
						FromName:  "handlers.Login",
						ToID:      1,
						ToName:    "AuthService.Login",
						Kind:      "call",
						SrcPath:   "internal/http/handlers/login.go",
						SrcLine:   28,
						Depth:     1,
						Direction: "in",
					},
				},
				Notes: []string{"depth clamped from 9 to 5 (see LIMITATIONS.md#graph-queries)"},
			},
		},
		{
			name:   "impact_analysis",
			method: "impact_analysis",
			result: query.Impact{
				Seed: seed,
				Hits: []query.ImpactHit{
					{
						ID:        2,
						Qualified: "handlers.Login",
						Kind:      "function",
						Path:      "internal/http/handlers/login.go",
						StartLine: 15,
						Distance:  1,
					},
					{
						ID:        4,
						Qualified: "router.Register",
						Kind:      "function",
						Path:      "internal/http/router.go",
						StartLine: 12,
						Distance:  2,
					},
				},
			},
		},
		{
			name:   "critical_path",
			method: "critical_path",
			result: query.CriticalPathResult{
				From: query.NeighborNode{
					ID:        2,
					Qualified: "handlers.Login",
					Kind:      "function",
					Path:      "internal/http/handlers/login.go",
					StartLine: 15,
				},
				To: query.NeighborNode{
					ID:        3,
					Qualified: "session.Mint",
					Kind:      "function",
					Path:      "internal/session/mint.go",
					StartLine: 30,
				},
				Paths: [][]query.PathVertex{
					{
						{ID: 2, Qualified: "handlers.Login", Kind: "function", Path: "internal/http/handlers/login.go", StartLine: 15},
						{ID: 1, Qualified: "AuthService.Login", Kind: "method", Path: "internal/auth/service.go", StartLine: 42},
						{ID: 3, Qualified: "session.Mint", Kind: "function", Path: "internal/session/mint.go", StartLine: 30},
					},
				},
				Notes: []string{"k clamped from 20 to 5"},
			},
		},
		{
			name:   "search_lexical",
			method: "search_lexical",
			result: query.SearchLexicalResult{
				Matches: []query.LexicalHit{
					{
						Path:    "internal/auth/service.go",
						Line:    61,
						Snippet: `    log.Printf("login failed for %s", creds.User)`,
					},
					{
						Path:    "internal/http/handlers/login.go",
						Line:    33,
						Snippet: `        http.Error(w, "login failed", http.StatusUnauthorized)`,
					},
				},
			},
		},
		{
			name:   "search_lexical_empty_hints",
			method: "search_lexical",
			result: query.SearchLexicalResult{
				Matches: []query.LexicalHit{},
				Hints: []string{
					`if "AuthServcie" is a symbol name, find_symbol("AuthServcie") searches the code graph and catches qualified forms/renames — search_lexical only sees literal text`,
					`2 indexed file(s) missing on disk — the index is stale; is the daemon running? ` + "`myco index`" + ` reconciles`,
				},
			},
		},
		{
			name:   "list_files",
			method: "list_files",
			result: query.ListFilesResult{
				Matches: []query.FileHit{
					{
						Path:        "internal/auth/service.go",
						Language:    "go",
						SymbolCount: 4,
						SizeBytes:   6144,
						LastIndexed: time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC),
					},
					{
						Path:        "internal/session/mint.go",
						Language:    "go",
						SymbolCount: 2,
						SizeBytes:   2048,
						LastIndexed: time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC),
					},
				},
			},
		},
		{
			name:   "find_document_key",
			method: "find_document_key",
			result: query.FindDocumentKeyResult{
				Matches: []query.DocumentHit{
					{
						ID:    1,
						Kind:  "yaml",
						Path:  ".mycelium.yml",
						Key:   "watcher.backend",
						Value: "watchman",
						Line:  12,
					},
					{
						ID:    2,
						Kind:  "json",
						Path:  "package.json",
						Key:   "scripts.build",
						Value: "tsc -p .",
						Line:  8,
					},
				},
			},
		},
		{
			// A full page plus the truncation hint: agents must be able to
			// tell "all the references" from "the first page of them".
			name:   "get_references_truncated",
			method: "get_references",
			result: query.GetReferencesResult{
				Matches: []query.ReferenceHit{
					{
						ID:            1,
						SrcPath:       "internal/http/handlers/login.go",
						SrcLine:       28,
						SrcSymbolName: "handlers.Login",
						DstName:       "Login",
						Kind:          "call",
						Resolved:      true,
					},
					{
						ID:      2,
						SrcPath: "internal/auth/service_test.go",
						SrcLine: 55,
						DstName: "Login",
						Kind:    "call",
					},
				},
				Hints: []string{
					"showing the first 2 matches — more exist; pass a larger limit to see them",
				},
			},
		},
		{
			name:   "search_lexical_definition_note",
			method: "search_lexical",
			result: query.SearchLexicalResult{
				Matches: []query.LexicalHit{
					{
						Path:    "internal/auth/service.go",
						Line:    42,
						Snippet: `func (s *AuthService) Login(ctx context.Context, creds Credentials) (*Session, error) {`,
					},
				},
			},
		},
		{
			name:   "read_focused",
			method: "read_focused",
			result: query.FocusedRead{
				Path:    "internal/auth/service.go",
				Focus:   "Login",
				Content: "package auth\n\nfunc (s *AuthService) Login(...) { ... }\n// [collapsed (lines 80-90): AuthService.Logout]\n",
				Stats: query.FocusedReadStats{
					TotalSymbols:    9,
					ExpandedSymbols: 2,
					OriginalBytes:   6242,
					ReturnedBytes:   1229,
				},
				Expanded: []query.FocusedSymbol{
					{Qualified: "AuthService.Login", Kind: "method", StartLine: 42, EndLine: 78},
					{Qualified: "AuthService.refresh", Kind: "method", StartLine: 92, EndLine: 101},
				},
			},
		},
		{
			name:   "read_focused_preview",
			method: "read_focused",
			result: query.FocusedRead{
				Path:    "internal/auth/service.go",
				Focus:   "",
				Content: "package auth\n\nimport (\n\t\"context\"\n)\n",
				Stats: query.FocusedReadStats{
					TotalSymbols:    9,
					ExpandedSymbols: 9,
					OriginalBytes:   6242,
					ReturnedBytes:   512,
				},
				Hint: `Preview only — first 50 of 240 lines shown. Pass focus=<query> (e.g. focus="Login") to filter to what you need.`,
				Expanded: []query.FocusedSymbol{
					{Qualified: "AuthService", Kind: "struct", StartLine: 20, EndLine: 90},
				},
			},
		},
		{
			name:   "read_focused_zero_expanded",
			method: "read_focused",
			result: query.FocusedRead{
				Path:    "internal/auth/service.go",
				Focus:   "NoSuchThing",
				Content: "// [collapsed (lines 1-240): entire file]\n",
				Stats: query.FocusedReadStats{
					TotalSymbols:    9,
					ExpandedSymbols: 0,
					OriginalBytes:   6242,
					ReturnedBytes:   40,
				},
			},
		},
		{
			name:   "stats",
			method: "stats",
			result: query.Stats{
				Files:                   120,
				Symbols:                 1450,
				Refs:                    5200,
				Resolved:                4900,
				NonImportRefs:           4600,
				RefsTypeResolved:        4200,
				RefsExternalKnown:       358,
				RefsTrulyUnresolved:     42,
				ByLang:                  map[string]int{"go": 90, "python": 10, "typescript": 20},
				DocumentsByKind:         map[string]int{"go.mod": 1, "json": 40},
				InterfaceImplementsRefs: 12,
				InterfaceConcreteTypes:  7,
				DBSizeBytes:             4 * 1024 * 1024,
				ConfiguredProjects: []query.ProjectStats{
					{Name: "backend", Root: "services/backend", FileCount: 80},
					{Name: "frontend", Root: "apps/web", FileCount: 40},
				},
				LastScan:     time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC),
				LastFullScan: time.Date(2026, 1, 2, 15, 10, 0, 0, time.UTC),
			},
		},
		{
			name:   "stats_empty",
			method: "stats",
			result: query.Stats{},
		},
		{
			name:   "unknown_method",
			method: "does_not_exist",
			result: map[string]any{"answer": 42, "detail": "unrenderable payload"},
		},
	}
}

func TestRenderGolden(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			got := Render(ipc.Method(tc.method), raw)

			path := filepath.Join("testdata", "golden", tc.name+".txt")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to generate): %v", err)
			}
			if got != string(want) {
				t.Errorf("render mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, want)
			}
		})
	}
}
