// Package check holds the pure logic of the structural verifier
// (`myco check` / `verify_changes`): diffing old-vs-current symbol sets
// and classifying dangling references. Orchestration (git, parsing,
// DB reads) lives in internal/service; this package stays deterministic
// and unit-testable.
package check

import (
	"sort"

	"github.com/datata1/mycelium/internal/query"
)

// Level mirrors doctor's severity scale.
type Level string

const (
	LevelPass Level = "pass"
	LevelWarn Level = "warn"
	LevelFail Level = "fail"
)

// OldSymbol is one symbol parsed from a file's base-commit version.
type OldSymbol struct {
	Qualified string
	Name      string
	Kind      string
	Path      string // repo-relative path the symbol lived in at base
}

// Removed is an old symbol whose qualified name no longer exists
// anywhere in the index — deleted or renamed without a survivor.
type Removed struct {
	Qualified string
	Name      string
	Kind      string
	OldPath   string
}

// Diff returns the old symbols whose qualified names are absent from
// the index-wide `exists` set (built by query.QualifiedExist over ALL
// old qualified names). The index-wide check is the false-positive
// guard: a symbol moved to another file — or defined identically in a
// second file — still resolves, so it is not "removed". Deduped by
// qualified name, stable order.
func Diff(old []OldSymbol, exists map[string]struct{}) []Removed {
	seen := map[string]struct{}{}
	var out []Removed
	for _, o := range old {
		if _, ok := exists[o.Qualified]; ok {
			continue
		}
		if _, dup := seen[o.Qualified]; dup {
			continue
		}
		seen[o.Qualified] = struct{}{}
		out = append(out, Removed{Qualified: o.Qualified, Name: o.Name, Kind: o.Kind, OldPath: o.Path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Qualified < out[j].Qualified })
	return out
}

// ClassifiedRemoved pairs a removed symbol with the dangling references
// that still point at it, exact matches first.
type ClassifiedRemoved struct {
	Removed
	Danglers []query.DanglingRef
	HasExact bool
}

// Classify attaches danglers to their removed symbols and derives the
// overall level: FAIL when any removed symbol has an exact-qualified
// dangler (that call site is broken), WARN when only short-name
// evidence exists, PASS when nothing dangles (clean deletions).
func Classify(removed []Removed, danglers []query.DanglingRef) ([]ClassifiedRemoved, Level) {
	byQualified := map[string]int{}
	byShort := map[string][]int{}
	out := make([]ClassifiedRemoved, len(removed))
	for i, rm := range removed {
		out[i] = ClassifiedRemoved{Removed: rm}
		byQualified[rm.Qualified] = i
		byShort[rm.Name] = append(byShort[rm.Name], i)
	}

	for _, d := range danglers {
		if d.Exact {
			if i, ok := byQualified[d.DstName]; ok {
				out[i].Danglers = append(out[i].Danglers, d)
				out[i].HasExact = true
			}
			continue
		}
		// Short-name evidence can belong to several removed symbols that
		// share a name; attach to each — the reader has to judge anyway.
		for _, i := range byShort[d.DstShort] {
			out[i].Danglers = append(out[i].Danglers, d)
		}
	}

	level := LevelPass
	for i := range out {
		sort.SliceStable(out[i].Danglers, func(a, b int) bool {
			da, db := out[i].Danglers[a], out[i].Danglers[b]
			if da.Exact != db.Exact {
				return da.Exact
			}
			if da.SrcPath != db.SrcPath {
				return da.SrcPath < db.SrcPath
			}
			return da.Line < db.Line
		})
		if out[i].HasExact {
			level = LevelFail
		} else if len(out[i].Danglers) > 0 && level != LevelFail {
			level = LevelWarn
		}
	}
	return out, level
}
