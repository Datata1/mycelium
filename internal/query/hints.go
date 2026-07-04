package query

import "fmt"

// findHintInput is the data buildFindHints needs to explain an empty
// FindSymbol result. The Reader fetches these fields (gated so misses
// don't pay for queries they don't need) and hands them here, keeping
// the hint-wording logic pure and unit-testable.
type findHintInput struct {
	name, kind, project string
	projectExists       bool     // meaningful only when project != ""
	configuredProjects  []string // all configured project names
	matchedKinds        []string // kinds matching name, ignoring the kind filter
	knownKinds          []string // all distinct kinds in the index
}

// buildFindHints returns 0..N human-readable lines explaining why a
// FindSymbol call produced no matches. Wording is unstable across v3.x;
// see CHANGELOG when it changes.
func buildFindHints(in findHintInput) []string {
	var hints []string

	// Project filter: did the named project exist at all?
	if in.project != "" && !in.projectExists {
		if len(in.configuredProjects) == 0 {
			hints = append(hints, fmt.Sprintf(
				"no project named %q — this index has no `projects:` block configured (single-project mode); omit the project filter or add the project to .mycelium.yml",
				in.project))
		} else {
			hints = append(hints, fmt.Sprintf(
				"no project named %q — configured projects: %s",
				in.project, formatList(in.configuredProjects)))
		}
	}

	// Kind filter: did the name match other kinds, or is the requested
	// kind absent from the index entirely?
	if in.kind != "" {
		switch {
		case len(in.matchedKinds) > 0:
			hints = append(hints, fmt.Sprintf(
				"name %q matches symbols of kind %s, but kind=%q eliminated them — drop the kind filter or try one of those",
				in.name, formatList(in.matchedKinds), in.kind))
		default:
			if len(in.knownKinds) > 0 && !contains(in.knownKinds, in.kind) {
				hints = append(hints, fmt.Sprintf(
					"no symbols of kind %q in this repo — known kinds: %s",
					in.kind, formatList(in.knownKinds)))
			}
		}
	}

	return hints
}

// buildRefsHints explains an empty GetReferences result. defCount is
// how many definitions matched the target (0 = unknown symbol);
// neverReconciled marks an index without a recorded full scan.
func buildRefsHints(target string, defCount int, neverReconciled bool) []string {
	var hints []string
	switch {
	case defCount == 0:
		hints = append(hints, fmt.Sprintf(
			"no symbol or reference named %q in the index — find_symbol(%q) does substring matching and catches qualified forms; check spelling/qualification",
			target, target))
	default:
		hints = append(hints, fmt.Sprintf(
			"symbol exists (%d definition(s)) but has no indexed references — possibly only reached via reflection/codegen, or dead code; get_neighborhood(%q) shows its outbound edges",
			defCount, target))
	}
	if neverReconciled {
		hints = append(hints, "index has never completed a full reconcile — run `myco index` or start `myco daemon`")
	}
	return hints
}

// buildLexicalHints explains an empty SearchLexical result.
// missingOnDisk counts index-known candidate files that no longer exist
// on disk.
func buildLexicalHints(pattern string, identifierShaped bool, missingOnDisk int) []string {
	var hints []string
	if identifierShaped {
		hints = append(hints, fmt.Sprintf(
			"if %q is a symbol name, find_symbol(%q) searches the code graph and catches qualified forms/renames — search_lexical only sees literal text",
			pattern, pattern))
	}
	if missingOnDisk > 0 {
		hints = append(hints, fmt.Sprintf(
			"%d indexed file(s) missing on disk — the index is stale; is the daemon running? `myco index` reconciles",
			missingOnDisk))
	}
	return hints
}
