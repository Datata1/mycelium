package parser

import "sync"

// Registry holds the set of parsers available at runtime and dispatches by file path.
type Registry struct {
	mu      sync.RWMutex
	parsers []Parser
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a parser. Order matters only if two parsers claim the same path.
func (r *Registry) Register(p Parser) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parsers = append(r.parsers, p)
}

// ForPath returns the first parser that supports the given path, or nil.
func (r *Registry) ForPath(path string) Parser {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.parsers {
		if p.Supports(path) {
			return p
		}
	}
	return nil
}

// Languages lists the language identifiers of every registered parser.
func (r *Registry) Languages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.parsers))
	for _, p := range r.parsers {
		out = append(out, p.Language())
	}
	return out
}
