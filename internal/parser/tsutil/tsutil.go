// Package tsutil has shared helpers for tree-sitter-backed parsers (TS, Python).
// It exists so per-language parsers don't re-implement node traversal, position
// extraction, and hash computation.
package tsutil

import (
	"crypto/sha256"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/jdwiederstein/mycelium/internal/parser"
)

// Slice returns the source text covered by a node.
func Slice(content []byte, n *sitter.Node) string {
	if n == nil {
		return ""
	}
	return string(content[n.StartByte():n.EndByte()])
}

// Position returns 1-based line/col, matching go/token conventions so all
// parsers emit the same coordinates.
func Position(n *sitter.Node) (startLine, startCol, endLine, endCol int) {
	if n == nil {
		return 0, 0, 0, 0
	}
	sp := n.StartPoint()
	ep := n.EndPoint()
	return int(sp.Row) + 1, int(sp.Column) + 1, int(ep.Row) + 1, int(ep.Column) + 1
}

// FirstChildByFieldName walks the immediate children looking for a child with
// a specific field name (as exposed by the grammar).
func FirstChildByFieldName(n *sitter.Node, field string) *sitter.Node {
	if n == nil {
		return nil
	}
	return n.ChildByFieldName(field)
}

// FirstChildByType returns the first direct child with the given type.
func FirstChildByType(n *sitter.Node, t string) *sitter.Node {
	if n == nil {
		return nil
	}
	for i := uint32(0); i < n.ChildCount(); i++ {
		c := n.Child(int(i))
		if c.Type() == t {
			return c
		}
	}
	return nil
}

// NamedChildren returns all *named* children (excludes punctuation/anon nodes).
func NamedChildren(n *sitter.Node) []*sitter.Node {
	if n == nil {
		return nil
	}
	out := make([]*sitter.Node, 0, n.NamedChildCount())
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(int(i)))
	}
	return out
}

// Walk is a pre-order traversal that stops descending when fn returns false.
func Walk(n *sitter.Node, fn func(*sitter.Node) bool) {
	if n == nil {
		return
	}
	if !fn(n) {
		return
	}
	for i := uint32(0); i < n.ChildCount(); i++ {
		Walk(n.Child(int(i)), fn)
	}
}

// SymbolHash is the canonical "body + signature" hash used everywhere to
// decide whether a symbol's embedding needs refreshing.
func SymbolHash(signature, body string) []byte {
	h := sha256.Sum256([]byte(signature + "\x00" + body))
	return h[:]
}

// ContentHash hashes the raw file bytes.
func ContentHash(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// ParseHash combines symbol + ref identifiers for a stable per-parse digest.
func ParseHash(symbols []parser.Symbol, refs []parser.Reference) []byte {
	h := sha256.New()
	for _, s := range symbols {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", s.Qualified, s.Kind, s.Signature)
	}
	for _, r := range refs {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", r.SrcSymbolQualified, r.DstName, r.Kind)
	}
	return h.Sum(nil)
}

// PrecedingComments returns the contiguous block of comments immediately
// before the target node (stopping at any non-comment sibling). Grammars
// expose comments as separate siblings rather than attached doc nodes, so
// both TypeScript and Python need this helper.
func PrecedingComments(content []byte, parent, target *sitter.Node, commentTypes map[string]bool) string {
	if parent == nil || target == nil {
		return ""
	}
	targetIdx := -1
	for i := uint32(0); i < parent.ChildCount(); i++ {
		if parent.Child(int(i)) == target {
			targetIdx = int(i)
			break
		}
	}
	if targetIdx <= 0 {
		return ""
	}
	start := targetIdx
	for i := targetIdx - 1; i >= 0; i-- {
		if commentTypes[parent.Child(i).Type()] {
			start = i
			continue
		}
		break
	}
	if start == targetIdx {
		return ""
	}
	var out []string
	for i := start; i < targetIdx; i++ {
		out = append(out, Slice(content, parent.Child(i)))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
