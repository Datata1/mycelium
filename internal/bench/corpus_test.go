package bench

import (
	"testing"

	"github.com/jdwiederstein/mycelium/internal/ipc"
)

// TestMyceliumDefaultCorpus_Wellformed pins the v4 B3 extracted corpus
// shape: every Case must have exactly one of FallbackCmd / FallbackFile
// set (the runner switches on that), every Case must have a Method
// matching one of the known IPC methods, and the corpus must cover
// the same nine tools the v3.4 calibrated multiplier set targets.
// A regression that drops a tool from the corpus would silently
// stop measuring its drift.
func TestMyceliumDefaultCorpus_Wellformed(t *testing.T) {
	t.Parallel()
	c := MyceliumDefaultCorpus()
	if c.Name == "" {
		t.Error("corpus Name empty — renderer header would lose the label")
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus Cases empty")
	}

	knownMethods := map[string]bool{
		ipc.MethodFindSymbol:      true,
		ipc.MethodGetReferences:   true,
		ipc.MethodReadFocused:     true,
		ipc.MethodGetFileOutline:  true,
		ipc.MethodGetFileSummary:  true,
		ipc.MethodSearchLexical:   true,
		ipc.MethodListFiles:       true,
		ipc.MethodImpactAnalysis:  true,
		ipc.MethodGetNeighborhood: true,
	}
	wantTools := map[string]bool{
		"find_symbol": true, "get_references": true, "read_focused": true,
		"get_file_outline": true, "get_file_summary": true,
		"search_lexical": true, "list_files": true,
		"impact_analysis": true, "get_neighborhood": true,
	}
	seen := map[string]bool{}
	for i, bc := range c.Cases {
		if !knownMethods[bc.Method] {
			t.Errorf("case %d: Method %q not in known IPC method list", i, bc.Method)
		}
		hasCmd := bc.FallbackCmd != ""
		hasFile := bc.FallbackFile != ""
		if hasCmd == hasFile {
			t.Errorf("case %d (%s): exactly one of FallbackCmd / FallbackFile must be set (cmd=%v file=%v)",
				i, bc.Tool, hasCmd, hasFile)
		}
		seen[bc.Tool] = true
	}
	for tool := range wantTools {
		if !seen[tool] {
			t.Errorf("corpus missing case for tool %q (would silently stop measuring its drift)", tool)
		}
	}
}
