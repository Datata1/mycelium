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

	"github.com/jdwiederstein/mycelium/internal/config"
	"github.com/jdwiederstein/mycelium/internal/query"
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
type Report struct {
	Checks []Check `json:"checks"`
	Summary struct {
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
	StaleWarn      int     // chunks without embedding but embedder configured
	StaleFail      int
	// Inotify headroom (Linux only): ratio of repo dirs to the system's
	// max_user_watches. Above InotifyWarn means the user is one big
	// `git clone` away from fsnotify silently dropping watches.
	InotifyWarn float64
	InotifyFail float64
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		UnresolvedWarn: 0.25,
		UnresolvedFail: 0.50,
		SelfLoopWarn:   1,
		SelfLoopFail:   20,
		FragmentWarn:   0.20,
		FragmentFail:   0.50,
		StaleWarn:      1,
		StaleFail:      1000,
		InotifyWarn:    0.50,
		InotifyFail:    0.90,
	}
}

// Run assembles the report. Callers pass the reader, the active embedder
// provider string (from config) so the stale-chunk check knows whether
// embeddings are expected at all, their preferred thresholds, and the
// repo root (for the inotify headroom check; empty skips the check).
func Run(ctx context.Context, r *query.Reader, embedderProvider string, th Thresholds, repoRoot string) (Report, error) {
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
			"resolution_bugs":  s.SelfLoopCount,
			"real_recursion":   s.RecursionSelfLoops,
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

	// Stale chunks: only meaningful when the user configured an embedder.
	// With provider=none, missing embeddings are expected — a stale count
	// of 100% is the normal state, not a bug.
	if embedderProvider == "" || embedderProvider == "none" {
		add(Check{
			Name:    "embedder",
			Level:   LevelPass,
			Message: "embedder disabled; semantic search unavailable",
		})
	} else {
		lvl := LevelPass
		switch {
		case s.StaleChunks >= th.StaleFail:
			lvl = LevelFail
		case s.StaleChunks >= th.StaleWarn:
			lvl = LevelWarn
		}
		add(Check{
			Name:    "chunks_embedded",
			Level:   lvl,
			Message: fmt.Sprintf("%d/%d chunks embedded (%d pending, queue depth %d)", s.ChunksEmbedded, s.Chunks, s.StaleChunks, s.EmbedQueueDepth),
			Detail: map[string]any{
				"embedded":    s.ChunksEmbedded,
				"total":       s.Chunks,
				"stale":       s.StaleChunks,
				"queue_depth": s.EmbedQueueDepth,
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

	return rep, nil
}

// ThresholdsFromConfig lets users override defaults via the embedder block
// in .mycelium.yml once we add a doctor section. For v1.1 we only consume
// the embedder provider and keep everything else at DefaultThresholds().
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
