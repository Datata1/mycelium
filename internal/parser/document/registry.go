// Package document contains the v3.3 document parsers — i18n JSON,
// package.json deps, go.mod requires. They emit DocumentEntry triples
// instead of a symbol graph and live behind the find_document_key
// MCP tool.
package document

import (
	"sync"

	"github.com/datata1/mycelium/internal/parser"
)

// Registry holds the set of document parsers available at runtime.
// Parallel to parser.Registry but for parser.DocumentParser.
type Registry struct {
	mu      sync.RWMutex
	parsers []parser.DocumentParser
}

// NewRegistry returns an empty document registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds a document parser. Order matters only if two parsers
// claim the same path; the first registered wins.
func (r *Registry) Register(p parser.DocumentParser) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parsers = append(r.parsers, p)
}

// ForPath returns the first document parser that supports the given
// path, or nil. Symbol-parser claims take precedence at the pipeline
// level; the document registry only sees files no symbol parser
// claims (or files matched in a separate document pass).
func (r *Registry) ForPath(path string) parser.DocumentParser {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.parsers {
		if p.Supports(path) {
			return p
		}
	}
	return nil
}

// Kinds returns the Kind() of every registered parser. Used by doctor
// to enumerate which document surfaces are wired up.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.parsers))
	for _, p := range r.parsers {
		out = append(out, p.Kind())
	}
	return out
}
