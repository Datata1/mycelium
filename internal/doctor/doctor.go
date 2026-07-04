// Package doctor runs health checks against a mycelium index and returns
// a report with per-check Pass/Warn/Fail status. Shared between the
// `myco doctor` CLI subcommand and any future HTTP/MCP introspection.
//
// The checks are deliberately cheap — they run against the live SQLite DB
// and don't re-parse or re-walk the source tree. The intended cadence is
// "run whenever you suspect the index is lying."
package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/datata1/mycelium/internal/config"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/telemetry"
)

// Level classifies a check result.
type Level string

const (
	LevelPass Level = "pass"
	LevelWarn Level = "warn"
	LevelFail Level = "fail"
)

// Check is one health check result.
type Check struct {
	Name    string `json:"name"`
	Level   Level  `json:"level"`
	Message string `json:"message"`
	// Numeric fields vary by check; kept as a generic map so future checks
	// can attach context without schema changes.
	Detail map[string]any `json:"detail,omitempty"`
}

// Report is the full doctor output.
//
// `Adoption` is the v4 B2 adoption-health section: per-failure-mode
// findings about how the agent is actually using myco vs. its
// fallbacks. **Adoption findings do NOT affect ExitCode** — they are
// informational and never gate CI. The renderer prints them in their
// own section so users (and CI consumers) can act on them without
// having to special-case adoption checks against everything else.
type Report struct {
	Checks   []Check           `json:"checks"`
	Adoption []AdoptionFinding `json:"adoption,omitempty"`
	Summary  struct {
		Pass int `json:"pass"`
		Warn int `json:"warn"`
		Fail int `json:"fail"`
	} `json:"summary"`
}

// ExitCode: 0 when all Pass, 1 when any Warn (but none Fail), 2 on any Fail.
// Mirrors conventional lint/test runner codes so CI gates read naturally.
func (r *Report) ExitCode() int {
	if r.Summary.Fail > 0 {
		return 2
	}
	if r.Summary.Warn > 0 {
		return 1
	}
	return 0
}

// Thresholds controls when a numeric check transitions between levels.
// All fields have sensible defaults from DefaultThresholds(); users can
// override in .mycelium.yml once we add schema support (v1.2+).
type Thresholds struct {
	UnresolvedWarn float64 // e.g. 0.25 — warn above 25% unresolved
	UnresolvedFail float64 // e.g. 0.50 — fail above 50%
	SelfLoopWarn   int     // any self-loop is a warn
	SelfLoopFail   int     // self-loops >= this number fail
	FragmentWarn   float64 // e.g. 0.20 — warn above 20% SQLite freelist
	FragmentFail   float64 // e.g. 0.50
	// Inotify headroom (Linux only): ratio of repo dirs to the system's
	// max_user_watches. Above InotifyWarn means the user is one big
	// `git clone` away from fsnotify silently dropping watches.
	InotifyWarn float64
	InotifyFail float64

	// v4 T2 layer 1: daemon fd-headroom (open fds vs RLIMIT_NOFILE
	// soft). WARN at 60% utilisation, FAIL at 90%. Pairs with v4 T2
	// layer 3's setrlimit-on-startup bump — together they preempt
	// the F1/T2 EMFILE failure mode on monorepo-scale repos.
	FDHeadroomWarn float64
	FDHeadroomFail float64
	// v3.1: per-project file-count thresholds for the
	// projects_configured_but_empty check. EmptyProjectFail = files
	// strictly below this fail (default 1, i.e. 0 files fails);
	// EmptyProjectWarn = files strictly below this warn (default 10,
	// likely a misconfigured include pattern but maybe a legitimately
	// tiny project).
	EmptyProjectFail int
	EmptyProjectWarn int

	// Freshness (sampled stat of indexed files vs disk). Warn from
	// FreshnessWarnCount stale/missing rows or FreshnessWarnRatio of
	// the sample, fail from FreshnessFailRatio — a branch switch the
	// daemon slept through shows up as a large ratio immediately.
	FreshnessWarnCount int
	FreshnessWarnRatio float64
	FreshnessFailRatio float64

	// v4 B2: adoption-health window + per-mode thresholds. Window
	// scopes which sessions count toward the evaluation; default 7d
	// matches the v4 ticket. The per-mode thresholds live in
	// AdoptionThresholds so adoption.go's pure evaluator can take
	// just that struct without depending on the rest of doctor.
	AdoptionWindow     time.Duration
	AdoptionThresholds AdoptionThresholds
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		UnresolvedWarn:   0.25,
		UnresolvedFail:   0.50,
		SelfLoopWarn:     1,
		SelfLoopFail:     20,
		FragmentWarn:     0.20,
		FragmentFail:     0.50,
		InotifyWarn:      0.50,
		InotifyFail:      0.90,
		FDHeadroomWarn:   0.60,
		FDHeadroomFail:   0.90,
		EmptyProjectFail: 1,
		EmptyProjectWarn: 10,

		FreshnessWarnCount: 2,
		FreshnessWarnRatio: 0.01,
		FreshnessFailRatio: 0.10,

		AdoptionWindow:     7 * 24 * time.Hour,
		AdoptionThresholds: DefaultAdoptionThresholds(),
	}
}

// Run assembles the report. repoRoot is used for the inotify headroom check
// (directory count); stateDir is where the daemon writes its PID file,
// telemetry log, and session files (usually .mycelium/ inside the repo, but
// may be an external path when the user configures ~/... index paths).
// Pass empty strings to skip those checks.
func Run(ctx context.Context, r *query.Reader, th Thresholds, repoRoot, stateDir string) (Report, error) {
	s, err := r.Stats(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("stats: %w", err)
	}
	rep := Report{}
	add := func(c Check) {
		rep.Checks = append(rep.Checks, c)
		switch c.Level {
		case LevelPass:
			rep.Summary.Pass++
		case LevelWarn:
			rep.Summary.Warn++
		case LevelFail:
			rep.Summary.Fail++
		}
	}

	// Corpus presence — the only hard-fail check. Everything else is a
	// quality signal on an existing index; this one catches "did you
	// actually run `myco index`?"
	if s.Files == 0 {
		add(Check{
			Name:    "corpus_present",
			Level:   LevelFail,
			Message: "index is empty — run `myco index` or start `myco daemon`",
		})
		// No point running the rest; return early.
		return rep, nil
	}
	add(Check{
		Name:    "corpus_present",
		Level:   LevelPass,
		Message: fmt.Sprintf("%d files, %d symbols, %d refs", s.Files, s.Symbols, s.Refs),
	})

	// Self-loops are a resolution bug; v1.2 type-aware resolver is designed
	// to kill them. Before then, any non-zero number is an expected warn.
	loopLevel := LevelPass
	switch {
	case s.SelfLoopCount >= th.SelfLoopFail:
		loopLevel = LevelFail
	case s.SelfLoopCount >= th.SelfLoopWarn:
		loopLevel = LevelWarn
	}
	add(Check{
		Name:  "self_loop_count",
		Level: loopLevel,
		Message: fmt.Sprintf(
			"%d resolution-bug self-loops (target 0); %d real recursion self-loops (informational)",
			s.SelfLoopCount, s.RecursionSelfLoops,
		),
		Detail: map[string]any{
			"resolution_bugs": s.SelfLoopCount,
			"real_recursion":  s.RecursionSelfLoops,
		},
	})

	// Unresolved ref ratio — the headline number for graph quality.
	ratio := s.UnresolvedRatio()
	ratioLevel := LevelPass
	switch {
	case ratio >= th.UnresolvedFail:
		ratioLevel = LevelFail
	case ratio >= th.UnresolvedWarn:
		ratioLevel = LevelWarn
	}
	add(Check{
		Name:  "unresolved_ref_ratio",
		Level: ratioLevel,
		Message: fmt.Sprintf(
			"%.1f%% of non-import refs are genuinely unresolved (%d/%d); %d known-external",
			ratio*100, s.RefsTrulyUnresolved, s.NonImportRefs, s.RefsExternalKnown,
		),
		Detail: map[string]any{
			"ratio":              ratio,
			"resolved_local":     s.Resolved,
			"external_known":     s.RefsExternalKnown,
			"truly_unresolved":   s.RefsTrulyUnresolved,
			"non_import_total":   s.NonImportRefs,
			"unresolved_by_lang": s.UnresolvedByLanguage,
		},
	})

	// Per-language unresolved breakdown — flagged at check-level if one
	// language is much worse than the whole.
	for lang, un := range s.UnresolvedByLanguage {
		total := s.TotalByLanguage[lang]
		if total == 0 {
			continue
		}
		langRatio := float64(un) / float64(total)
		lvl := LevelPass
		switch {
		case langRatio >= th.UnresolvedFail:
			lvl = LevelFail
		case langRatio >= th.UnresolvedWarn:
			lvl = LevelWarn
		}
		add(Check{
			Name:    "unresolved_" + lang,
			Level:   lvl,
			Message: fmt.Sprintf("%s: %.1f%% unresolved (%d/%d)", lang, langRatio*100, un, total),
			Detail: map[string]any{
				"language":   lang,
				"ratio":      langRatio,
				"unresolved": un,
				"total":      total,
			},
		})
	}

	// SQLite fragmentation proxy. freelist / page_count >= 20% usually
	// means a VACUUM would recover meaningful disk space.
	frag := s.DBFragmentation()
	fragLevel := LevelPass
	switch {
	case frag >= th.FragmentFail:
		fragLevel = LevelFail
	case frag >= th.FragmentWarn:
		fragLevel = LevelWarn
	}
	add(Check{
		Name:    "db_fragmentation",
		Level:   fragLevel,
		Message: fmt.Sprintf("%.1f%% free pages (%d / %d; db=%s)", frag*100, s.DBFreelistPages, s.DBPageCount, formatBytes(s.DBSizeBytes)),
		Detail: map[string]any{
			"fragmentation": frag,
			"freelist":      s.DBFreelistPages,
			"page_count":    s.DBPageCount,
			"size_bytes":    s.DBSizeBytes,
		},
	})

	// Inotify headroom — Linux-only heuristic for "will fsnotify fail to
	// register all my dirs?" Skipped on other OSes and when repoRoot is
	// empty (e.g. tests running against a bare DB).
	if repoRoot != "" {
		if c := inotifyCheck(repoRoot, th); c != nil {
			add(*c)
		}
	}

	// Freshness: sampled stat of indexed rows against the working tree.
	// The cheap answer to "is the index lying about what's on disk?" —
	// `myco doctor --deep` upgrades it to an exact walk diff.
	if c := freshnessCheck(ctx, r, th, repoRoot); c != nil {
		add(*c)
	}
	{
		// Resolve the state dir: explicit stateDir > repoRoot/.mycelium fallback.
		sd := stateDir
		if sd == "" && repoRoot != "" {
			sd = filepath.Join(repoRoot, ".mycelium")
		}
		if sd != "" {
			// v4 T2 layer 1: read the daemon's PID file (written by
			// runDaemon) and probe /proc/<pid>/fd to surface fd-headroom
			// pressure before EMFILE hits. Linux-only; no-op stub on
			// macOS / Windows.
			if c := daemonFDHeadroomCheck(sd, th); c != nil {
				add(*c)
			}
		}
	}

	// v2.1: interface-implementer linkage signal. Pure informational —
	// always pass — but the count tells the user whether RefInherit
	// edges are populated. Zero is suspicious on a Go repo with
	// interfaces but isn't a hard fail; the resolver may simply not be
	// loaded on the user's machine. Linked to Chinthareddy 2026's
	// interface-consumer expansion via `get_neighborhood`.
	add(Check{
		Name:  "interface_expansion_coverage",
		Level: LevelPass,
		Message: fmt.Sprintf(
			"%d concrete types linked to interfaces via %d RefInherit edges",
			s.InterfaceConcreteTypes, s.InterfaceImplementsRefs,
		),
		Detail: map[string]any{
			"concrete_types": s.InterfaceConcreteTypes,
			"inherit_refs":   s.InterfaceImplementsRefs,
		},
	})

	// v3.1: workspace project configuration. Skipped when no
	// projects: block is set (single-project mode, the default). When
	// configured, every project should have indexed files; zero is a
	// fail (the include pattern matched nothing or the project root
	// is wrong) and a small handful warns (likely a too-narrow
	// include glob, but might be a legitimately tiny project).
	if len(s.ConfiguredProjects) > 0 {
		var fails, warns []string
		for _, p := range s.ConfiguredProjects {
			switch {
			case p.FileCount < th.EmptyProjectFail:
				fails = append(fails, fmt.Sprintf("%s (0 files; root=%s)", p.Name, p.Root))
			case p.FileCount < th.EmptyProjectWarn:
				warns = append(warns, fmt.Sprintf("%s (%d files)", p.Name, p.FileCount))
			}
		}
		level := LevelPass
		msg := fmt.Sprintf("%d configured project(s) all populated", len(s.ConfiguredProjects))
		switch {
		case len(fails) > 0:
			level = LevelFail
			msg = fmt.Sprintf("empty project(s): %v — likely an include/root misconfiguration in .mycelium.yml", fails)
		case len(warns) > 0:
			level = LevelWarn
			msg = fmt.Sprintf("project(s) with very few files: %v", warns)
		}
		add(Check{
			Name:    "projects_configured_but_empty",
			Level:   level,
			Message: msg,
			Detail: map[string]any{
				"configured": s.ConfiguredProjects,
				"fails":      fails,
				"warns":      warns,
			},
		})
	}

	// v3.3 documents coverage. Reports per-kind entry counts and flags
	// any document_kind row in `files` whose `documents` join is empty
	// — that's the symptom of a parser that claimed the file but
	// produced zero entries (config error or unknown JSON shape).
	if len(s.DocumentsByKind) > 0 || r.HasDocumentFiles(ctx) {
		parts := []string{}
		for _, k := range sortedDocKindKeys(s.DocumentsByKind) {
			parts = append(parts, fmt.Sprintf("%s:%d", k, s.DocumentsByKind[k]))
		}
		level := LevelPass
		msg := "documents indexed: " + strings.Join(parts, " ")
		if len(parts) == 0 {
			msg = "document files present but no entries extracted"
		}
		empty := r.EmptyDocumentFiles(ctx, 5)
		if len(empty) > 0 {
			level = LevelWarn
			msg = fmt.Sprintf("documents indexed: %s; %d file(s) registered but produced 0 entries",
				strings.Join(parts, " "), len(empty))
		}
		add(Check{
			Name:    "documents_indexed",
			Level:   level,
			Message: msg,
			Detail: map[string]any{
				"by_kind":     s.DocumentsByKind,
				"empty_files": empty,
			},
		})
	}

	// v4 B2: adoption-health section. Filled when repoRoot is set
	// and there's telemetry to evaluate. Findings live on rep.Adoption
	// so they don't roll into rep.Summary (informational, never gate
	// CI). The legacy v3.4 telemetry-dark-spot warning still fires as
	// a regular Check because "telemetry off in a dogfood repo" is a
	// configuration bug, not an adoption insight.
	{
		sd := stateDir
		if sd == "" && repoRoot != "" {
			sd = filepath.Join(repoRoot, ".mycelium")
		}
		if sd != "" {
			rep.Adoption = evaluateAdoptionForStateDir(sd, th)
			if c := checkTelemetryDarkSpot(sd); c != nil {
				add(*c)
			}
		}
	}

	return rep, nil
}

// evaluateAdoptionForStateDir is the I/O wrapper around EvaluateAdoption:
// reads the windowed myco + fallback summaries from stateDir (the directory
// that holds telemetry.jsonl and session_*_external.jsonl files), counts
// distinct sessions in the window, and hands the result to the pure evaluator.
// Empty (or no-telemetry) state dirs return nil.
func evaluateAdoptionForStateDir(stateDir string, th Thresholds) []AdoptionFinding {
	mDir := stateDir
	since := time.Time{}
	if th.AdoptionWindow > 0 {
		since = time.Now().Add(-th.AdoptionWindow)
	}

	mycoLog := filepath.Join(mDir, "telemetry.jsonl")
	myco, err := telemetry.AggregateSince(mycoLog, since)
	if err != nil {
		return nil
	}

	fallback, err := telemetry.SummarizeAllExternalSince(mDir, since)
	if err != nil {
		// Fallback-side missing isn't fatal — we still evaluate the
		// modes that depend only on the myco-side counts.
		fallback = nil
	}

	sessions := countSessionsInWindow(mycoLog, since)
	if len(myco) == 0 && len(fallback) == 0 && sessions == 0 {
		// Nothing to say; let the dark-spot check handle the
		// "configured but empty" diagnosis instead of duplicating it.
		return nil
	}
	return EvaluateAdoption(myco, fallback, sessions, th.AdoptionThresholds)
}

// countSessionsInWindow counts distinct sid values in the telemetry
// log whose first record falls in the window. The MinSessions gate in
// EvaluateAdoption uses this number to decide whether enough data has
// accumulated to draw conclusions.
func countSessionsInWindow(logPath string, since time.Time) int {
	reports, err := telemetry.ListSessions(logPath)
	if err != nil {
		return 0
	}
	if since.IsZero() {
		return len(reports)
	}
	n := 0
	for _, r := range reports {
		if !r.Session.StartedAt.Before(since) {
			n++
		}
	}
	return n
}

// sortedDocKindKeys returns the keys of m in lexical order. Stable
// doctor output; useful for tests too.
func sortedDocKindKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkTelemetryDarkSpot detects the dogfooding gap surfaced by the
// v3.4 Go field test (finding G5): the session-tracking hook is
// active and writing fallback-tool logs (`session_*_external.jsonl`),
// but `telemetry.jsonl` is missing or empty — so we have the
// fallback half of agent behaviour but not the myco-call half.
// Returns nil when neither stream is present (a quiet repo, no
// adoption story yet) or when both are present (telemetry on, all
// good).
func checkTelemetryDarkSpot(stateDir string) *Check {
	mDir := stateDir
	hasSessions := false
	entries, err := os.ReadDir(mDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "_external.jsonl") {
			hasSessions = true
			break
		}
	}
	if !hasSessions {
		return nil
	}
	// Sessions exist; is telemetry pulling its weight?
	telPath := filepath.Join(mDir, "telemetry.jsonl")
	info, err := os.Stat(telPath)
	telemetryActive := err == nil && info.Size() > 0
	if telemetryActive {
		return nil
	}
	return &Check{
		Name:  "telemetry_dark_spot",
		Level: LevelWarn,
		Message: "session hooks are recording fallback tools but telemetry.jsonl is empty/missing — " +
			"enable `telemetry.enabled: true` in .mycelium.yml so myco-call counts get captured too",
		Detail: map[string]any{
			"sessions_dir":     mDir,
			"telemetry_jsonl":  telPath,
			"telemetry_active": telemetryActive,
			"hint":             "without telemetry on, `myco session export <id>` shows myco calls: 0 even on heavy usage",
		},
	}
}

// ThresholdsFromConfig returns defaults from the config. Reserved for future
// per-repo threshold overrides via .mycelium.yml.
func ThresholdsFromConfig(_ config.Config) Thresholds {
	return DefaultThresholds()
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
