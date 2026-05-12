package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jdwiederstein/mycelium/internal/parser"
)

// I18NJSONParser handles i18n locale files. It walks the JSON object
// tree, joining nested keys with "." and recording the line of each
// string leaf so agents can jump directly to the entry with
// read_focused.
//
// Only string values become entries. Numbers, booleans, and null are
// skipped — i18n values are always strings in practice and the extra
// noise hurts FTS quality. Arrays use numeric indices in the key
// (foo.items.0.name).
type I18NJSONParser struct{}

// New returns a ready-to-register I18NJSONParser. Cheap; the parser is
// stateless.
func NewI18NJSON() *I18NJSONParser { return &I18NJSONParser{} }

// Kind reports the document kind written to files.document_kind and
// documents.kind for entries this parser produces.
func (p *I18NJSONParser) Kind() string { return "i18n_json" }

// Supports matches .json files under a "locales" or "i18n" directory
// segment. Deliberately narrow: arbitrary JSON files should NOT be
// indexed as i18n (package.json has its own parser, fixtures and
// configs are out of scope).
func (p *I18NJSONParser) Supports(path string) bool {
	s := filepath.ToSlash(path)
	if !strings.HasSuffix(s, ".json") {
		return false
	}
	return strings.Contains(s, "/locales/") ||
		strings.HasPrefix(s, "locales/") ||
		strings.Contains(s, "/i18n/") ||
		strings.HasPrefix(s, "i18n/")
}

// Parse flattens the JSON tree into (key, value, line) entries.
// Returns an empty (but valid) DocumentResult for empty/null inputs
// rather than an error — an empty locale file is unusual but legal.
func (p *I18NJSONParser) Parse(ctx context.Context, path string, content []byte) (parser.DocumentResult, error) {
	res := parser.DocumentResult{
		Path:        path,
		Kind:        p.Kind(),
		ContentHash: contentHash(content),
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return res, nil
	}

	w := &tokenWalker{
		dec:       json.NewDecoder(bytes.NewReader(content)),
		lineStart: indexLineStarts(content),
	}
	w.dec.UseNumber()
	if err := w.walkValue(""); err != nil {
		return res, fmt.Errorf("parse i18n %s: %w", path, err)
	}
	// Deterministic order — keeps test assertions and reindex churn
	// (parse_hash stability) honest.
	sort.SliceStable(w.entries, func(i, j int) bool {
		return w.entries[i].Key < w.entries[j].Key
	})
	res.Entries = w.entries
	return res, nil
}

// tokenWalker consumes a JSON token stream maintaining a key stack
// (for dotted-path flattening) and resolving each leaf-string's byte
// offset to a 1-based line number via the precomputed lineStart
// index.
type tokenWalker struct {
	dec       *json.Decoder
	keyStack  []string
	entries   []parser.DocumentEntry
	lineStart []int
}

// walkValue consumes one JSON value (object, array, or primitive).
// For object/array openers it recurses into walkObject / walkArray;
// for string leaves it emits an entry; other primitives are skipped.
// The current key stack determines the entry's flat key.
func (w *tokenWalker) walkValue(_ string) error {
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return w.walkObject()
		case '[':
			return w.walkArray()
		default:
			return fmt.Errorf("unexpected delimiter %q", v)
		}
	case string:
		// Decoder.InputOffset points to the byte just past the
		// consumed token. Step back into the token to find its
		// starting line; off-by-one in either direction would
		// still report the same line for typical i18n files
		// (one entry per line), so we settle for the token's
		// trailing offset which is what the API gives us cheaply.
		line := w.lineOf(w.dec.InputOffset())
		w.entries = append(w.entries, parser.DocumentEntry{
			Key:   strings.Join(w.keyStack, "."),
			Value: v,
			Line:  line,
		})
	}
	// numbers / booleans / nulls intentionally ignored.
	return nil
}

func (w *tokenWalker) walkObject() error {
	for w.dec.More() {
		keyTok, err := w.dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("non-string object key %v", keyTok)
		}
		w.keyStack = append(w.keyStack, key)
		if err := w.walkValue(key); err != nil {
			return err
		}
		w.keyStack = w.keyStack[:len(w.keyStack)-1]
	}
	// Consume the closing '}'.
	if _, err := w.dec.Token(); err != nil {
		return err
	}
	return nil
}

func (w *tokenWalker) walkArray() error {
	idx := 0
	for w.dec.More() {
		w.keyStack = append(w.keyStack, strconv.Itoa(idx))
		if err := w.walkValue(""); err != nil {
			return err
		}
		w.keyStack = w.keyStack[:len(w.keyStack)-1]
		idx++
	}
	// Consume the closing ']'.
	if _, err := w.dec.Token(); err != nil {
		return err
	}
	return nil
}

// lineOf returns the 1-based line number for the given byte offset
// via binary search over the line-start index. Falls back to line 1
// when the offset is at/before the first byte.
func (w *tokenWalker) lineOf(off int64) int {
	if len(w.lineStart) == 0 || off <= 0 {
		return 1
	}
	// Find the largest lineStart entry <= off; its index+1 is the line.
	lo, hi := 0, len(w.lineStart)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if int64(w.lineStart[mid]) <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

// indexLineStarts records the byte offset where each line begins.
// Line 1 starts at byte 0; line N starts immediately after the
// (N-1)th newline.
func indexLineStarts(content []byte) []int {
	out := []int{0}
	for i, b := range content {
		if b == '\n' {
			out = append(out, i+1)
		}
	}
	return out
}

func contentHash(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
