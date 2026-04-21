package chunker

import (
	"crypto/sha256"
	"strings"

	"github.com/jdwiederstein/mycelium/internal/parser"
)

// Chunk is what the indexer writes and the embedder consumes.
// Content is the text that will be fed into an embedding model; ContentHash
// is the dedup key that lets reformats / reorders skip re-embedding.
type Chunk struct {
	SymbolQualified string // "" for window chunks (future)
	Kind            string // "symbol" | "window"
	StartLine       int
	EndLine         int
	Content         string
	ContentHash     []byte
}

// Options tune the chunking output. Defaults are intentionally conservative:
// small symbols with no docstring aren't worth an embedding call.
type Options struct {
	MinContentChars int  // drop chunks smaller than this (default 40)
	MaxContentChars int  // truncate long chunks (default 4000, ~1000 tokens)
	IncludeDocs     bool // prepend docstring to content (default true)
}

func DefaultOptions() Options {
	return Options{MinContentChars: 40, MaxContentChars: 4000, IncludeDocs: true}
}

// FromSymbols turns a parsed file's symbols into one Chunk per symbol worth
// embedding. The raw file content is needed because Symbol doesn't carry the
// body text — that's intentionally kept out of the parser struct so per-symbol
// storage stays small in the common case.
func FromSymbols(content []byte, symbols []parser.Symbol, opts Options) []Chunk {
	if opts.MinContentChars == 0 && opts.MaxContentChars == 0 {
		opts = DefaultOptions()
	}
	out := make([]Chunk, 0, len(symbols))
	for _, s := range symbols {
		// Consts, vars, interfaces are usually small one-liners. Embed them
		// only if they have a docstring (worth the recall).
		if skipKind(s) && s.Docstring == "" {
			continue
		}
		body := sliceLines(content, s.StartLine, s.EndLine)
		text := buildChunkText(s, body, opts)
		if len(text) < opts.MinContentChars {
			continue
		}
		if len(text) > opts.MaxContentChars {
			text = text[:opts.MaxContentChars]
		}
		h := sha256.Sum256([]byte(text))
		out = append(out, Chunk{
			SymbolQualified: s.Qualified,
			Kind:            "symbol",
			StartLine:       s.StartLine,
			EndLine:         s.EndLine,
			Content:         text,
			ContentHash:     h[:],
		})
	}
	return out
}

func skipKind(s parser.Symbol) bool {
	switch s.Kind {
	case parser.KindVar, parser.KindConst:
		return true
	}
	return false
}

func buildChunkText(s parser.Symbol, body string, opts Options) string {
	var b strings.Builder
	b.WriteString(s.Qualified)
	b.WriteString("\n")
	if s.Signature != "" {
		b.WriteString(s.Signature)
		b.WriteString("\n")
	}
	if opts.IncludeDocs && s.Docstring != "" {
		b.WriteString(s.Docstring)
		b.WriteString("\n")
	}
	b.WriteString(body)
	return b.String()
}

// sliceLines extracts a [start,end] 1-based inclusive range from content.
// Tolerates out-of-range values by clamping — parsers occasionally emit
// positions past EOF on the last symbol.
func sliceLines(content []byte, startLine, endLine int) string {
	if startLine < 1 {
		startLine = 1
	}
	line := 1
	var startIdx, endIdx int
	startIdx = -1
	for i := 0; i < len(content); i++ {
		if line == startLine && startIdx == -1 {
			startIdx = i
		}
		if content[i] == '\n' {
			line++
			if line > endLine {
				endIdx = i
				break
			}
		}
	}
	if startIdx == -1 {
		return ""
	}
	if endIdx == 0 {
		endIdx = len(content)
	}
	return string(content[startIdx:endIdx])
}
