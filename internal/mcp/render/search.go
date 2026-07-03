package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datata1/mycelium/internal/ipc"
)

func Lexical(raw json.RawMessage) string {
	var hits []ipc.LexicalHit
	if err := json.Unmarshal(raw, &hits); err != nil {
		return RawJSON(raw)
	}
	if len(hits) == 0 {
		return "no matches"
	}
	var sb strings.Builder
	for _, h := range hits {
		snippet := strings.TrimSpace(h.Snippet)
		fmt.Fprintf(&sb, "%s:%d:\t%s\n", h.Path, h.Line, snippet)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func ListFiles(raw json.RawMessage) string {
	var files []ipc.FileHit
	if err := json.Unmarshal(raw, &files); err != nil {
		return RawJSON(raw)
	}
	if len(files) == 0 {
		return "no files"
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
		return "no matches"
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "%-30s  %s:%d  %s\n", h.Key, h.Path, h.Line, h.Value)
	}
	return strings.TrimRight(sb.String(), "\n")
}
