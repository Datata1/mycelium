package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

func Lexical(raw json.RawMessage) string {
	var r ipc.SearchLexicalResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return RawJSON(raw)
	}
	hits := r.Matches
	if len(hits) == 0 {
		if len(r.Hints) > 0 {
			return "no matches\nhints:\n  " + strings.Join(r.Hints, "\n  ")
		}
		return "no matches"
	}
	var sb strings.Builder
	snippets := make([]string, len(hits))
	for i, h := range hits {
		snippet := strings.TrimSpace(h.Snippet)
		snippets[i] = snippet
		fmt.Fprintf(&sb, "%s:%d:\t%s\n", h.Path, h.Line, snippet)
	}
	// A search landing on a definition line is the myco-as-grep failure
	// mode in the act: point back at the code graph right here.
	if note, ok := lexicalDefinitionNote(snippets); ok {
		sb.WriteString(note)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func ListFiles(raw json.RawMessage) string {
	var files []ipc.FileHit
	if err := json.Unmarshal(raw, &files); err != nil {
		return RawJSON(raw)
	}
	if len(files) == 0 {
		return "no files match — drop the name_contains/language filters, or run stats to see what languages are indexed"
	}
	var sb strings.Builder
	for _, f := range files {
		fmt.Fprintf(&sb, "%-60s  %-8s  %d symbols\n", f.Path, f.Language, f.SymbolCount)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func DocumentKey(raw json.RawMessage) string {
	var hits []ipc.DocumentHit
	if err := json.Unmarshal(raw, &hits); err != nil {
		return RawJSON(raw)
	}
	if len(hits) == 0 {
		return "no matches — keys match by substring over indexed documents (i18n JSON, package.json deps, go.mod requires); stats shows documents_by_kind"
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "%-30s  %s:%d  %s\n", h.Key, h.Path, h.Line, h.Value)
	}
	return strings.TrimRight(sb.String(), "\n")
}
