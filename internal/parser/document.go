package parser

import "context"

// DocumentEntry is one (key, value) pair extracted from a document
// file (i18n JSON, package.json deps, go.mod requires). Documents are
// the v3.3 parallel surface to the symbol graph — they have no refs
// and never participate in code-graph queries; agents reach them via
// find_document_key.
type DocumentEntry struct {
	Key   string
	Value string
	Line  int
}

// DocumentResult is what a DocumentParser emits per file. ContentHash
// drives the same "skip writes when nothing changed" path that
// ParseResult uses for symbol parsers.
type DocumentResult struct {
	Path        string
	Kind        string
	Entries     []DocumentEntry
	ContentHash []byte
}

// DocumentParser is the document-side counterpart to Parser. Implementations
// emit (key, value, line) triples instead of a symbol graph; there is no
// Language() method since documents aren't programming languages. Safe for
// concurrent use.
type DocumentParser interface {
	Kind() string
	Supports(path string) bool
	Parse(ctx context.Context, path string, content []byte) (DocumentResult, error)
}
