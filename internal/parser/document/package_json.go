package document

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/datata1/mycelium/internal/parser"
)

// PackageJSONParser extracts dependency entries from `package.json`
// files. Only the four standard dependency sections are surfaced
// (`dependencies`, `devDependencies`, `peerDependencies`,
// `optionalDependencies`); everything else in the file (scripts,
// engines, browser, …) is ignored. v3.3 cuts the surface intentionally
// narrow — agent failures in the field test were "find where this
// dependency is declared," not "what scripts does this package run."
type PackageJSONParser struct{}

// NewPackageJSON returns a ready-to-register parser.
func NewPackageJSON() *PackageJSONParser { return &PackageJSONParser{} }

// Kind reports the document kind for entries.
func (p *PackageJSONParser) Kind() string { return "package_json_deps" }

// Supports matches files whose basename is exactly `package.json`.
// Locking on the basename keeps surface narrow and forecloses on the
// obvious accidental hits (e.g. `path/to/my-package.json`).
func (p *PackageJSONParser) Supports(path string) bool {
	return filepath.Base(filepath.ToSlash(path)) == "package.json"
}

// dependencySections is the closed set of keys whose contents become
// document entries. Section disambiguation (dev vs runtime vs peer)
// is not encoded in the entry today — most agent queries are "where
// is this dep?", not "is it dev?". If a future field test shows that
// gap, surface the section as a prefix or a side field.
var dependencySections = []string{
	"dependencies",
	"devDependencies",
	"peerDependencies",
	"optionalDependencies",
}

// Parse walks the four dependency sections and emits one entry per
// (package name, version range) pair. Other top-level keys are
// skipped without error so the parser tolerates the long tail of
// package.json conventions.
func (p *PackageJSONParser) Parse(ctx context.Context, path string, content []byte) (parser.DocumentResult, error) {
	res := parser.DocumentResult{
		Path:        path,
		Kind:        p.Kind(),
		ContentHash: contentHash(content),
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return res, nil
	}

	// Decode the whole file once with a permissive shape — every
	// section is `map[string]string`, anything else (object, array,
	// number) is skipped. Using `json.RawMessage` lets us probe
	// section presence without unmarshalling sibling keys we don't
	// care about.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(content, &top); err != nil {
		return res, fmt.Errorf("parse package.json %s: %w", path, err)
	}

	lineStart := indexLineStarts(content)
	var entries []parser.DocumentEntry
	for _, section := range dependencySections {
		raw, ok := top[section]
		if !ok || len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		entries = append(entries, sectionEntries(content, lineStart, raw)...)
	}
	// Deterministic ordering keeps parse_hash stable.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
	res.Entries = entries
	return res, nil
}

// sectionEntries decodes one dependency section into entries with
// per-key line numbers. Uses the same token-walker pattern as the
// i18n parser but with a single-level flat shape — section objects
// can't legally nest in package.json.
func sectionEntries(content []byte, lineStart []int, sectionRaw json.RawMessage) []parser.DocumentEntry {
	// The RawMessage starts somewhere in the file. We need its
	// in-file offset to translate the inner Decoder's offsets into
	// absolute file lines. Search the content for the section's
	// bytes; in practice the RawMessage is a unique substring of the
	// file (each section object's opening "{" is at one specific
	// position).
	startOffset := bytes.Index(content, sectionRaw)
	if startOffset < 0 {
		startOffset = 0
	}

	dec := json.NewDecoder(bytes.NewReader(sectionRaw))
	dec.UseNumber()
	// Consume the opening '{'.
	if _, err := dec.Token(); err != nil {
		return nil
	}

	var out []parser.DocumentEntry
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return out
		}
		key, ok := keyTok.(string)
		if !ok {
			return out
		}
		valTok, err := dec.Token()
		if err != nil {
			return out
		}
		val, ok := valTok.(string)
		if !ok {
			continue // skip non-string ranges (rare; legal package.json values can be objects in workspaces:)
		}
		// dec.InputOffset() is the offset within the RawMessage
		// slice; add the section's file-level start offset.
		absOffset := int64(startOffset) + dec.InputOffset()
		line := lineFromOffset(lineStart, absOffset)
		out = append(out, parser.DocumentEntry{
			Key:   key,
			Value: val,
			Line:  line,
		})
	}
	return out
}

// lineFromOffset is the binary-search line lookup shared with the
// i18n parser's tokenWalker. Repeated here so package_json doesn't
// take a hidden dependency on tokenWalker's internals; cheap.
func lineFromOffset(lineStart []int, off int64) int {
	if len(lineStart) == 0 || off <= 0 {
		return 1
	}
	lo, hi := 0, len(lineStart)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if int64(lineStart[mid]) <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}
