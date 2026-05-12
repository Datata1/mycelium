package document

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"golang.org/x/mod/modfile"

	"github.com/jdwiederstein/mycelium/internal/parser"
)

// GoModParser surfaces require directives from `go.mod` files.
// `replace` and `exclude` directives are out of scope for v3.3 —
// they're rare in agent-facing workflows. Adding them later is a
// drop-in.
type GoModParser struct{}

// NewGoMod returns a ready-to-register parser.
func NewGoMod() *GoModParser { return &GoModParser{} }

// Kind reports the document kind for entries.
func (p *GoModParser) Kind() string { return "go_mod_requires" }

// Supports matches files whose basename is exactly `go.mod`.
func (p *GoModParser) Supports(path string) bool {
	return filepath.Base(filepath.ToSlash(path)) == "go.mod"
}

// Parse delegates to `golang.org/x/mod/modfile` for parsing — that
// package handles `// indirect` comments, block vs. single-line
// requires, and version-string validation uniformly. Each require
// entry becomes one DocumentEntry keyed by module path with value =
// version string. The `Indirect` flag is preserved by appending
// `  // indirect` to the value so agents can distinguish first-party
// from transitive deps without a separate field.
func (p *GoModParser) Parse(ctx context.Context, path string, content []byte) (parser.DocumentResult, error) {
	res := parser.DocumentResult{
		Path:        path,
		Kind:        p.Kind(),
		ContentHash: contentHash(content),
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return res, nil
	}
	mf, err := modfile.Parse(path, content, nil)
	if err != nil {
		return res, fmt.Errorf("parse go.mod %s: %w", path, err)
	}
	var entries []parser.DocumentEntry
	for _, r := range mf.Require {
		val := r.Mod.Version
		if r.Indirect {
			val += "  // indirect"
		}
		entries = append(entries, parser.DocumentEntry{
			Key:   r.Mod.Path,
			Value: val,
			Line:  r.Syntax.Start.Line,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
	res.Entries = entries
	return res, nil
}
