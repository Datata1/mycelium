package skills

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jdwiederstein/mycelium/internal/query"
)

// AspectSpec declares one cross-cutting aspect: how to fetch matching
// symbols from the index, plus the metadata stamped into the
// generated INDEX.md frontmatter so agents can judge how much to
// trust the list.
type AspectSpec struct {
	// Name is the slug used as the directory name under aspects/.
	Name string
	// Description is one line, surfaced in frontmatter and the index
	// table at the root.
	Description string
	// Heuristic flags filters that match on outbound refs / textual
	// signals rather than authoritative type info. Surfaced as
	// `heuristic: true` in frontmatter so agents discount the list.
	Heuristic bool
	// Match is the per-aspect query. It owns the choice between
	// SymbolsBySignatureLike and SymbolsByOutboundRef and any
	// language scoping.
	Match func(ctx context.Context, r Reader, limit int) ([]query.AspectMatch, error)
}

// builtinAspects is the v2.3 fixed list. Per the v3 plan we cap at a
// curated short list (≤10) — adding new aspects is a deliberate
// PR-gated decision, not a per-repo config knob.
//
// Two are clean (signature-driven, Go-only) and two are heuristic
// (ref-driven, Go-only). TS/Python aspect coverage is deferred to
// v2.5.
var builtinAspects = []AspectSpec{
	{
		Name:        "error-handling",
		Description: "Symbols whose Go signature returns error.",
		Heuristic:   false,
		Match: func(ctx context.Context, r Reader, limit int) ([]query.AspectMatch, error) {
			// "% error" catches `func f() error`, `(T, error)`,
			// `(err error)`. "% error)" catches the named-return
			// edge case `(err error)` in the middle of a tuple.
			return r.SymbolsBySignatureLike(ctx, "go", []string{"% error", "% error)"}, limit)
		},
	},
	{
		Name:        "context-propagation",
		Description: "Symbols that take or return context.Context (Go).",
		Heuristic:   false,
		Match: func(ctx context.Context, r Reader, limit int) ([]query.AspectMatch, error) {
			return r.SymbolsBySignatureLike(ctx, "go", []string{"%context.Context%"}, limit)
		},
	},
	{
		Name:        "config-loading",
		Description: "Symbols with at least one outbound ref into internal/config.",
		Heuristic:   true,
		Match: func(ctx context.Context, r Reader, limit int) ([]query.AspectMatch, error) {
			return r.SymbolsByOutboundRef(ctx, "go", "internal/config", "", limit)
		},
	},
	{
		Name:        "logging",
		Description: "Symbols that call into stdlib log.* or a *Logger interface.",
		Heuristic:   true,
		Match: func(ctx context.Context, r Reader, limit int) ([]query.AspectMatch, error) {
			// Stdlib log refs land as dst_name = "log.Printf",
			// "log.Fatal" etc. with dst_symbol_id NULL. Internal
			// loggers (daemon.Logger.Printf and friends) land as
			// dst_name ending in "Logger.<method>" — match on suffix.
			return r.SymbolsByOutboundRef(ctx, "go", "", "log.%", limit)
		},
	},
}

// AspectsLimit caps each aspect's INDEX.md row count. Sized to "fits
// on one screen" rather than "exhaustive list" — the latter is what
// MCP queries are for.
const AspectsLimit = 100

// renderAspect returns the INDEX.md byte stream for one aspect.
// Output is byte-deterministic given a stable Reader response.
func renderAspect(ctx context.Context, r Reader, spec AspectSpec, opts Options) (string, int, error) {
	matches, err := spec.Match(ctx, r, AspectsLimit)
	if err != nil {
		return "", 0, err
	}
	var b strings.Builder
	fmt.Fprintln(&b, "---")
	fmt.Fprintf(&b, "name: %s\n", spec.Name)
	fmt.Fprintf(&b, "description: %s\n", spec.Description)
	fmt.Fprintln(&b, "level: aspect")
	fmt.Fprintf(&b, "heuristic: %t\n", spec.Heuristic)
	fmt.Fprintf(&b, "matches: %d\n", len(matches))
	fmt.Fprintf(&b, "limit: %d\n", AspectsLimit)
	fmt.Fprintf(&b, "generated: %s\n", opts.Now.Format(time.RFC3339))
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "# aspect: %s\n\n", spec.Name)
	fmt.Fprintln(&b, spec.Description)
	if spec.Heuristic {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "_Heuristic filter — false positives possible. Use `myco query refs <symbol>` to verify._")
	}
	fmt.Fprintln(&b)
	if len(matches) == 0 {
		fmt.Fprintln(&b, "_No matches in this index._")
		return b.String(), 0, nil
	}
	if len(matches) >= AspectsLimit {
		fmt.Fprintf(&b, "Showing the top %d matches by inbound ref count. The full list may be longer.\n\n", AspectsLimit)
	}
	fmt.Fprintln(&b, "| Symbol | Package | Inbound | Signature |")
	fmt.Fprintln(&b, "|--------|---------|---------|-----------|")
	for _, m := range matches {
		pkg := displayDir(path.Dir(m.Path))
		sig := collapseSpace(m.Signature)
		// Markdown-table cell escaping: pipes inside cells terminate
		// the cell. Replace with the HTML entity so signatures with
		// `|` (rare in Go but possible in TS unions) survive.
		sig = strings.ReplaceAll(sig, "|", "\\|")
		fmt.Fprintf(&b, "| `%s` | %s | %d | `%s` |\n",
			m.Qualified, pkg, m.InboundRefs, sig)
	}
	return b.String(), len(matches), nil
}
