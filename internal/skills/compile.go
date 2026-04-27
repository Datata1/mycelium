package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jdwiederstein/mycelium/internal/query"
)

// Reader is the subset of *internal/query.Reader the generator needs.
// Declaring it as an interface (instead of taking the concrete type)
// keeps the test fakes light — golden tests don't need a full SQLite
// index, they need deterministic outline + summary + ref counts.
type Reader interface {
	ListFiles(ctx context.Context, language, nameContains, project string, limit int, pathsIn []string) ([]query.FileHit, error)
	GetFileSummary(ctx context.Context, path string) (query.FileSummary, error)
	GetFileOutline(ctx context.Context, path, focus string) ([]query.FileOutlineItem, error)
	PackageRefAggregates(ctx context.Context, pkgDir string, limit int) (inbound, outbound []query.PackageRefAgg, err error)
	SymbolsBySignatureLike(ctx context.Context, language string, patterns []string, limit int) ([]query.AspectMatch, error)
	SymbolsByOutboundRef(ctx context.Context, language, dstFilePrefix, dstNameLike string, limit int) ([]query.AspectMatch, error)
}

// Store is the v2.5 hash-gating surface. *index.Index satisfies this
// interface; tests pass an in-memory fake. When Store is nil on
// Options, Compile renders and writes every file unconditionally —
// the v2.3 behaviour preserved for callers that don't care about
// modtime churn.
type Store interface {
	SkillFileHash(ctx context.Context, path string) (string, error)
	UpsertSkillFile(ctx context.Context, path, hash string) error
	DeleteSkillFile(ctx context.Context, path string) error
	PruneSkillFiles(ctx context.Context, keep []string) error
}

// Options control a Compile run. Zero values give a sensible default
// (whole tree, real wall-clock timestamp).
type Options struct {
	OutDir string
	// PackageFilter, when non-empty, restricts emission to a single
	// package directory (e.g. "internal/query"). Used by
	// `myco skills compile --package` for fast iteration when
	// debugging template output.
	PackageFilter string
	// AspectFilter mirrors PackageFilter for the aspects subtree.
	// Empty = emit all aspects. Phase 3 only.
	AspectFilter string
	// Now is injected so tests can pin the `generated:` frontmatter
	// timestamp. Zero -> time.Now().UTC().
	Now time.Time
	// TopRefLimit caps the inbound/outbound tables in each SKILL.md.
	// Default 20.
	TopRefLimit int
	// Store gates writes to disk by hash (v2.5). Nil = always write.
	Store Store
	// Stats, when non-nil, is populated with per-call counts so
	// callers can report "rendered 28, wrote 0" on a clean tree
	// without re-walking the output dir.
	Stats *Stats
	// DryRun makes Compile render + hash every output but skip both
	// the os.WriteFile and the Store.UpsertSkillFile call. Combined
	// with Stats it powers `myco skills compile --status`. Requires
	// Store to be set (otherwise there's nothing to compare against).
	DryRun bool
}

// Stats is the optional per-call write summary. Populated by Compile
// + RegenerateAffected when Options.Stats is non-nil.
type Stats struct {
	Rendered int // total files rendered (including no-ops)
	Written  int // files whose hash differed and were rewritten
	Skipped  int // files whose hash matched and were skipped
	Pruned   int // skill_files rows removed for orphan packages
}

// Compile walks the index via r and writes the skills tree under
// opts.OutDir. Existing files in OutDir are overwritten; orphaned
// files (a package was renamed and its old SKILL.md is now stale)
// are removed at the end of a full run, but skipped when a filter
// is set.
func Compile(ctx context.Context, r Reader, opts Options) error {
	if opts.OutDir == "" {
		return fmt.Errorf("skills: OutDir is required")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if opts.TopRefLimit <= 0 {
		opts.TopRefLimit = 20
	}

	// AspectFilter narrows the run to a single aspect — skip the
	// package walk entirely so users iterating on aspect output
	// don't pay the per-package cost.
	var pkgs []pkgUnit
	if opts.AspectFilter == "" {
		var err error
		pkgs, err = discoverPackages(ctx, r)
		if err != nil {
			return fmt.Errorf("discover packages: %w", err)
		}
		if opts.PackageFilter != "" {
			filtered := pkgs[:0]
			for _, p := range pkgs {
				if p.Dir == opts.PackageFilter {
					filtered = append(filtered, p)
				}
			}
			pkgs = filtered
		}
	}

	var written []emitted
	// emittedPaths tracks every tree-relative output path this run
	// renders. Used by PruneSkillFiles when the run is unfiltered so
	// orphaned hash rows get cleaned up.
	var emittedPaths []string

	for _, p := range pkgs {
		body, totalSyms, topLevelN, renderErr := renderPackageSkill(ctx, r, p, opts)
		if renderErr != nil {
			return fmt.Errorf("render %s: %w", p.Dir, renderErr)
		}
		rel := skillRelPath("packages/" + packageOutPath(p.Dir) + "/SKILL.md")
		if err := writeIfChanged(ctx, opts, rel, body); err != nil {
			return err
		}
		emittedPaths = append(emittedPaths, rel)
		written = append(written, emitted{Unit: p, TotalSyms: totalSyms, TopLevelN: topLevelN})
	}

	// Aspects subtree. Skipped entirely when a package filter is set
	// (the user asked for one package, don't surprise them with whole-
	// tree aspect regen).
	var aspectsEmitted []aspectEmitted
	if opts.PackageFilter == "" {
		for _, spec := range builtinAspects {
			if opts.AspectFilter != "" && spec.Name != opts.AspectFilter {
				continue
			}
			body, n, err := renderAspect(ctx, r, spec, opts)
			if err != nil {
				return fmt.Errorf("render aspect %s: %w", spec.Name, err)
			}
			rel := skillRelPath("aspects/" + spec.Name + "/INDEX.md")
			if err := writeIfChanged(ctx, opts, rel, body); err != nil {
				return err
			}
			emittedPaths = append(emittedPaths, rel)
			aspectsEmitted = append(aspectsEmitted, aspectEmitted{Spec: spec, MatchCount: n})
		}
	}

	// Skip the root index when any filter is active: it'd be misleading
	// to overwrite a whole-tree index with a one-package or one-aspect view.
	if opts.PackageFilter == "" && opts.AspectFilter == "" {
		body := renderRootIndex(written, aspectsEmitted, opts)
		rel := skillRelPath("INDEX.md")
		if err := writeIfChanged(ctx, opts, rel, body); err != nil {
			return err
		}
		emittedPaths = append(emittedPaths, rel)

		// Prune orphan hash rows + on-disk SKILL.md / INDEX.md files
		// that aren't in the kept set. Only safe on an unfiltered full
		// run. DryRun skips both — the caller is asking what *would*
		// change, not committing to it.
		if !opts.DryRun {
			if opts.Store != nil {
				if err := opts.Store.PruneSkillFiles(ctx, emittedPaths); err != nil {
					return fmt.Errorf("prune skill_files: %w", err)
				}
			}
			if err := pruneOrphanFiles(opts.OutDir, emittedPaths); err != nil {
				return fmt.Errorf("prune disk orphans: %w", err)
			}
		}
	}
	return nil
}

// RegenerateAffected is the daemon-driven incremental entry point. It
// takes the set of package directories the watcher's most recent batch
// touched and regenerates the skills tree.
//
// For v2.5 this delegates to Compile() with the Store set: the hash
// gate inside writeIfChanged ensures unchanged files are not rewritten,
// so the cost on a clean tree is just the render work (~100 ms on the
// self-index). The packages parameter is captured for telemetry and to
// reserve the API surface for a future per-package short-circuit;
// today it is informational.
//
// Why not skip the discovery walk entirely? The root INDEX.md needs
// every package's symbol counts, and aspects pull from across the whole
// repo. Both depend on cross-package state that any one touched file
// can invalidate (the v3 plan calls this "cross-package ref-count
// drift"). Re-rendering is cheaper than tracking that dependency graph.
func RegenerateAffected(ctx context.Context, r Reader, opts Options, packages []string) error {
	if opts.Store == nil {
		return fmt.Errorf("skills: RegenerateAffected requires Options.Store (incremental needs the hash gate)")
	}
	_ = packages // informational for v2.5; reserved for per-package short-circuit
	return Compile(ctx, r, opts)
}

// writeIfChanged renders one skill file. When opts.Store is set it
// hashes the body and skips the WriteFile when the stored hash
// matches; otherwise it always writes (v2.3 behaviour). Either way it
// makes the parent dir and updates opts.Stats when present.
func writeIfChanged(ctx context.Context, opts Options, relPath, body string) error {
	if opts.Stats != nil {
		opts.Stats.Rendered++
	}
	abs := filepath.Join(opts.OutDir, filepath.FromSlash(relPath))
	hash := hashBody(body)
	skip := false
	if opts.Store != nil {
		stored, err := opts.Store.SkillFileHash(ctx, relPath)
		if err != nil {
			return err
		}
		// Belt-and-suspenders: even if the DB says we wrote this hash,
		// a missing file on disk (someone rm'd the tree) needs a real
		// write. Skipped under DryRun — the caller is asking "would my
		// real tree change?", not about a transient temp dir.
		if stored == hash {
			if opts.DryRun {
				skip = true
			} else if _, statErr := os.Stat(abs); statErr == nil {
				skip = true
			}
		}
	}
	if skip {
		if opts.Stats != nil {
			opts.Stats.Skipped++
		}
		return nil
	}
	if opts.DryRun {
		// Count as a would-write without touching disk or the store.
		if opts.Stats != nil {
			opts.Stats.Written++
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		return err
	}
	if opts.Stats != nil {
		opts.Stats.Written++
	}
	if opts.Store != nil {
		if err := opts.Store.UpsertSkillFile(ctx, relPath, hash); err != nil {
			return err
		}
	}
	return nil
}

// hashBody is the canonical hash function for skill_files. SHA-256 is
// overkill for a 2KB markdown blob but the cost is negligible and we
// avoid a new dependency or the risk of a 32-bit collision in a
// long-lived corpus.
//
// The wall-clock `generated:` frontmatter line is excluded so two
// renders with the same structural content but different timestamps
// produce the same hash. Without this the hash gate would fire on
// every daemon batch, defeating the v2.5 incremental story.
func hashBody(body string) string {
	h := sha256.New()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "generated:") {
			continue
		}
		h.Write([]byte(line))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// skillRelPath normalises an output path to forward slashes. The DB
// stores tree-relative paths and we want them stable across OSes.
func skillRelPath(p string) string {
	return filepath.ToSlash(p)
}

// pruneOrphanFiles removes SKILL.md / INDEX.md files under OutDir
// that aren't in the kept set. Other files (handwritten notes, future
// extension files) are left alone.
func pruneOrphanFiles(outDir string, kept []string) error {
	keepSet := make(map[string]bool, len(kept))
	for _, k := range kept {
		keepSet[k] = true
	}
	return filepath.Walk(outDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		if base != "SKILL.md" && base != "INDEX.md" {
			return nil
		}
		rel, err := filepath.Rel(outDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !keepSet[rel] {
			return os.Remove(p)
		}
		return nil
	})
}

// aspectEmitted records one written aspect for the root index.
type aspectEmitted struct {
	Spec       AspectSpec
	MatchCount int
}

// packageOutPath maps a package directory to the on-disk subpath
// under packages/. The repo root maps to "_root" so we never try to
// write packages/SKILL.md (which would collide with the directory).
func packageOutPath(dir string) string {
	if dir == "." || dir == "" {
		return "_root"
	}
	return dir
}

// emitted records what Compile wrote for one package. Carried back to
// renderRootIndex so the index reflects exactly what's on disk
// (avoids drift between SKILL.md frontmatter and the index counts).
type emitted struct {
	Unit      pkgUnit
	TotalSyms int
	TopLevelN int
}

// renderRootIndex builds the top-level INDEX.md: one row per emitted
// SKILL.md, sorted by directory, plus an aspects table linking to the
// cross-cutting subtree. Acts as the entry point an agent reads first.
func renderRootIndex(units []emitted, aspects []aspectEmitted, opts Options) string {
	var b strings.Builder
	totalFiles, totalSyms := 0, 0
	for _, u := range units {
		totalFiles += len(u.Unit.Files)
		totalSyms += u.TotalSyms
	}
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b, "name: mycelium-skills")
	fmt.Fprintln(&b, "description: Browseable directory of every package's SKILL.md.")
	fmt.Fprintln(&b, "level: index")
	fmt.Fprintf(&b, "package_count: %d\n", len(units))
	fmt.Fprintf(&b, "file_count: %d\n", totalFiles)
	fmt.Fprintf(&b, "symbol_count: %d\n", totalSyms)
	fmt.Fprintf(&b, "generated: %s\n", opts.Now.Format(time.RFC3339))
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "# Mycelium skills tree")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Each row links to a per-package SKILL.md under `packages/`.")
	fmt.Fprintln(&b, "Read the SKILL.md for an overview; use the MCP tools (or")
	fmt.Fprintln(&b, "`myco query …`) for specific reference / impact / search queries.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Packages")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Package | Language | Files | Symbols |")
	fmt.Fprintln(&b, "|---------|----------|-------|---------|")
	for _, u := range units {
		link := "packages/" + packageOutPath(u.Unit.Dir) + "/SKILL.md"
		fmt.Fprintf(&b, "| [%s](%s) | %s | %d | %d |\n",
			displayDir(u.Unit.Dir), link, u.Unit.Language,
			len(u.Unit.Files), u.TotalSyms)
	}
	if len(aspects) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "## Aspects (cross-cutting)")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "| Aspect | Matches | Heuristic? | Description |")
		fmt.Fprintln(&b, "|--------|---------|------------|-------------|")
		for _, a := range aspects {
			link := "aspects/" + a.Spec.Name + "/INDEX.md"
			heuristic := "no"
			if a.Spec.Heuristic {
				heuristic = "yes"
			}
			fmt.Fprintf(&b, "| [%s](%s) | %d | %s | %s |\n",
				a.Spec.Name, link, a.MatchCount, heuristic, a.Spec.Description)
		}
	}
	return b.String()
}

// pkgUnit is a single emission target — one directory containing
// indexed source files. For Go this lines up with the language's
// package concept; for TS/Python (which lack a directory-level
// package primitive) it's just "all source files in this folder."
// Files preserves the order ListFiles returned (stable: by path).
type pkgUnit struct {
	Dir       string          // forward-slash repo-relative
	Language  string          // dominant language, or "mixed"
	Languages []string        // sorted, deduplicated
	Files     []query.FileHit // every indexed file in Dir, any language
}

// discoverPackages groups every indexed file by parent directory and
// returns one pkgUnit per directory. When a directory contains files
// of more than one language (rare but legal — e.g. a Go package with
// embedded TS assets), Language is "mixed" and Languages lists every
// language sorted alphabetically.
func discoverPackages(ctx context.Context, r Reader) ([]pkgUnit, error) {
	files, err := r.ListFiles(ctx, "", "", "", 100000, nil)
	if err != nil {
		return nil, err
	}
	type group struct {
		files []query.FileHit
		langs map[string]int
	}
	byDir := map[string]*group{}
	for _, f := range files {
		d := path.Dir(f.Path)
		g, ok := byDir[d]
		if !ok {
			g = &group{langs: map[string]int{}}
			byDir[d] = g
		}
		g.files = append(g.files, f)
		g.langs[f.Language]++
	}
	out := make([]pkgUnit, 0, len(byDir))
	for d, g := range byDir {
		langs := make([]string, 0, len(g.langs))
		var dominant string
		var dominantN int
		for l, n := range g.langs {
			langs = append(langs, l)
			if n > dominantN || (n == dominantN && l < dominant) {
				dominant = l
				dominantN = n
			}
		}
		sort.Strings(langs)
		lang := dominant
		if len(langs) > 1 {
			lang = "mixed"
		}
		out = append(out, pkgUnit{Dir: d, Language: lang, Languages: langs, Files: g.files})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

// symEntry is one row in the symbol table rendered into SKILL.md.
type symEntry struct {
	Name      string
	Qualified string
	Kind      string
	Path      string
	Line      int
	Signature string
}

// renderPackageSkill produces the full SKILL.md byte stream for one
// package and returns it alongside the total + top-level symbol
// counts (so renderRootIndex can populate the index without re-reading
// the file). Output is deterministic: every list is sorted by a stable
// key and timestamps come from opts.Now.
func renderPackageSkill(ctx context.Context, r Reader, p pkgUnit, opts Options) (string, int, int, error) {
	var symbols []symEntry
	totalSymbolCount := 0
	imports := map[string]bool{}

	for _, f := range p.Files {
		summary, err := r.GetFileSummary(ctx, f.Path)
		if err != nil {
			return "", 0, 0, err
		}
		totalSymbolCount += summary.SymbolCount
		for _, imp := range summary.Imports {
			imports[imp] = true
		}
		outline, err := r.GetFileOutline(ctx, f.Path, "")
		if err != nil {
			return "", 0, 0, err
		}
		// Top-level symbols only; methods are reachable via their
		// receiver type and would balloon the lean SKILL.md.
		for _, item := range outline {
			symbols = append(symbols, symEntry{
				Name:      item.Name,
				Qualified: item.Qualified,
				Kind:      item.Kind,
				Path:      f.Path,
				Line:      item.StartLine,
				Signature: item.Signature,
			})
		}
	}

	// Stable sort: kind first (so the table groups), then by qualified
	// name. Empty signatures sort the same as filled ones.
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Kind != symbols[j].Kind {
			return symbols[i].Kind < symbols[j].Kind
		}
		return symbols[i].Qualified < symbols[j].Qualified
	})

	inbound, outbound, err := r.PackageRefAggregates(ctx, p.Dir, opts.TopRefLimit)
	if err != nil {
		return "", 0, 0, err
	}

	var b strings.Builder
	// Frontmatter — fixed key order for determinism.
	fmt.Fprintln(&b, "---")
	fmt.Fprintf(&b, "name: %s\n", p.Dir)
	fmt.Fprintf(&b, "description: %s\n", oneLineDescription(p, symbols))
	fmt.Fprintln(&b, "level: package")
	fmt.Fprintf(&b, "language: %s\n", p.Language)
	fmt.Fprintf(&b, "files: %d\n", len(p.Files))
	fmt.Fprintf(&b, "symbols: %d\n", totalSymbolCount)
	fmt.Fprintf(&b, "top_level_symbols: %d\n", len(symbols))
	fmt.Fprintf(&b, "generated: %s\n", opts.Now.Format(time.RFC3339))
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)

	fmt.Fprintf(&b, "# %s\n\n", p.Dir)

	// Body
	if len(symbols) == 0 {
		fmt.Fprintln(&b, "_No top-level symbols indexed in this package._")
		return b.String(), totalSymbolCount, len(symbols), nil
	}

	// Group by kind for the symbol section.
	byKind := map[string][]symEntry{}
	kinds := []string{}
	for _, s := range symbols {
		if _, ok := byKind[s.Kind]; !ok {
			kinds = append(kinds, s.Kind)
		}
		byKind[s.Kind] = append(byKind[s.Kind], s)
	}
	sort.Strings(kinds)
	fmt.Fprintf(&b, "## Top-level symbols (%d)\n\n", len(symbols))
	for _, k := range kinds {
		fmt.Fprintf(&b, "### %s\n", titleCase(k))
		for _, s := range byKind[k] {
			line := fmt.Sprintf("- `%s` — %s:%d", s.Name, s.Path, s.Line)
			if strings.TrimSpace(s.Signature) != "" {
				line += fmt.Sprintf("\n  - signature: `%s`", collapseSpace(s.Signature))
			}
			fmt.Fprintln(&b, line)
		}
		fmt.Fprintln(&b)
	}

	// Inbound table.
	fmt.Fprintln(&b, "## Top inbound (callers of this package)")
	if len(inbound) == 0 {
		fmt.Fprintln(&b, "_None._")
	} else {
		for _, agg := range inbound {
			fmt.Fprintf(&b, "- %s — %d refs\n", displayDir(agg.Path), agg.RefCount)
		}
	}
	fmt.Fprintln(&b)

	// Outbound table.
	fmt.Fprintln(&b, "## Top outbound (packages this package calls into)")
	if len(outbound) == 0 {
		fmt.Fprintln(&b, "_None._")
	} else {
		for _, agg := range outbound {
			fmt.Fprintf(&b, "- %s — %d refs\n", displayDir(agg.Path), agg.RefCount)
		}
	}
	fmt.Fprintln(&b)

	// See-also pointer keeps the lean/complementary contract honest:
	// SKILL.md tells you the shape; MCP gives you the details.
	fmt.Fprintln(&b, "## See also")
	fmt.Fprintln(&b, "- For specific reference sites: `myco query refs <symbol>`")
	fmt.Fprintln(&b, "- For neighborhood exploration: `myco query neighbors <symbol>`")

	return b.String(), totalSymbolCount, len(symbols), nil
}

// oneLineDescription is a placeholder until we surface package-level
// docstrings from the index. For v2.3 we synthesize from the file +
// symbol counts to give a non-empty, deterministic hint.
func oneLineDescription(p pkgUnit, syms []symEntry) string {
	if len(syms) == 0 {
		return fmt.Sprintf("Package %s (%d files, no top-level symbols).", path.Base(p.Dir), len(p.Files))
	}
	return fmt.Sprintf("Package %s (%d files, %d top-level symbols).", path.Base(p.Dir), len(p.Files), len(syms))
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// displayDir renders a directory path for the inbound/outbound tables.
// path.Dir returns "." for repo-root files (e.g. "main.go" or
// "integration_test.go"); rendering "." would confuse a reader.
func displayDir(d string) string {
	if d == "." || d == "" {
		return "(repo root)"
	}
	return d
}
