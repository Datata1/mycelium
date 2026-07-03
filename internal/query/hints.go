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
