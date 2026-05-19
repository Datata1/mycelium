package render

import (
	"encoding/json"
)

// Render formats a daemon JSON result for LLM consumption.
// Falls back to indented JSON if unmarshal fails.
func Render(method string, raw json.RawMessage) string {
	switch method {
	case "find_symbol":
		return renderFindSymbol(raw)
	case "get_file_outline":
		return renderFileOutline(raw)
	case "get_file_summary":
		return renderFileSummary(raw)
	case "get_references":
		return renderReferences(raw)
	case "get_neighborhood":
		return renderNeighborhood(raw)
	case "impact_analysis":
		return renderImpact(raw)
	case "critical_path":
		return renderCriticalPath(raw)
	case "search_lexical":
		return renderLexical(raw)
	case "list_files":
		return renderListFiles(raw)
	case "find_document_key":
		return renderDocumentKey(raw)
	case "stats":
		return renderStats(raw)
	}
	return fallback(raw)
}

func fallback(raw json.RawMessage) string {
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}
