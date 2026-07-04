package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/datata1/mycelium/internal/query"
)

// freshnessSampleSize bounds the default check: 200 os.Stat calls are
// sub-millisecond and keep doctor's "deliberately cheap" contract.
const freshnessSampleSize = 200

// freshnessCheck samples indexed files and stats them on disk, counting
// rows whose file is gone (should have been pruned) or whose disk mtime
// is newer than what indexing recorded (a change the daemon never saw).
// Returns nil when repoRoot is empty (bare-DB tests).
func freshnessCheck(ctx context.Context, r *query.Reader, th Thresholds, repoRoot string) *Check {
	if repoRoot == "" {
		return nil
	}
	sample, err := r.SampleFiles(ctx, freshnessSampleSize)
	if err != nil || len(sample) == 0 {
		return nil
	}
	var missing, stale int
	for _, row := range sample {
		abs := filepath.Join(repoRoot, filepath.FromSlash(row.ProjectRoot), filepath.FromSlash(row.Path))
		info, err := os.Stat(abs)
		switch {
		case err != nil:
			missing++
		case info.ModTime().UnixNano() > row.MTimeNS:
			stale++
		}
	}
	bad := missing + stale
	ratio := float64(bad) / float64(len(sample))

	lastScan := "no reconcile recorded"
	if t, ok, err := r.LastFullScanAt(ctx); err == nil && ok {
		lastScan = fmt.Sprintf("last reconcile %s ago", time.Since(t).Round(time.Second))
	}

	level := LevelPass
	msg := fmt.Sprintf("sampled %d files: all fresh on disk (%s)", len(sample), lastScan)
	if bad > 0 {
		msg = fmt.Sprintf(
			"sampled %d files: %d missing on disk, %d modified since indexing (%s) — is the daemon running? `myco index` reconciles",
			len(sample), missing, stale, lastScan)
		switch {
		case ratio >= th.FreshnessFailRatio:
			level = LevelFail
		case bad >= th.FreshnessWarnCount || ratio >= th.FreshnessWarnRatio:
			level = LevelWarn
		}
	}
	return &Check{
		Name:    "index_freshness",
		Level:   level,
		Message: msg,
		Detail: map[string]any{
			"sampled":         len(sample),
			"missing_on_disk": missing,
			"mtime_newer":     stale,
			"ratio":           ratio,
		},
	}
}

// Add appends a check to the report and rolls it into the summary. Used
// by callers that compute extra checks outside Run (doctor --deep).
func (r *Report) Add(c Check) {
	r.Checks = append(r.Checks, c)
	switch c.Level {
	case LevelPass:
		r.Summary.Pass++
	case LevelWarn:
		r.Summary.Warn++
	case LevelFail:
		r.Summary.Fail++
	}
}

// DeepFreshness is the exact (walk-based) counterpart to the sampled
// index_freshness check: set-diff of the paths the current configuration
// walks against the paths in the index. Walked and indexed are both
// project-relative (the files.path key space). exampleCap bounds how
// many example paths land in the message.
func DeepFreshness(walked map[string]struct{}, indexed []string, exampleCap int) Check {
	indexedSet := make(map[string]struct{}, len(indexed))
	for _, p := range indexed {
		indexedSet[p] = struct{}{}
	}
	var notIndexed, ghost []string
	for p := range walked {
		if _, ok := indexedSet[p]; !ok {
			notIndexed = append(notIndexed, p)
		}
	}
	for _, p := range indexed {
		if _, ok := walked[p]; !ok {
			ghost = append(ghost, p)
		}
	}
	sort.Strings(notIndexed)
	sort.Strings(ghost)

	level := LevelPass
	msg := fmt.Sprintf("deep walk diff clean: %d walked, %d indexed", len(walked), len(indexed))
	if len(notIndexed) > 0 || len(ghost) > 0 {
		level = LevelWarn
		msg = fmt.Sprintf(
			"deep walk diff: %d on disk but not indexed %v; %d indexed but not walked %v — `myco index` reconciles",
			len(notIndexed), truncateExamples(notIndexed, exampleCap),
			len(ghost), truncateExamples(ghost, exampleCap))
	}
	return Check{
		Name:    "index_freshness_deep",
		Level:   level,
		Message: msg,
		Detail: map[string]any{
			"walked":                    len(walked),
			"indexed":                   len(indexed),
			"on_disk_not_indexed":       truncateExamples(notIndexed, exampleCap),
			"indexed_not_walked":        truncateExamples(ghost, exampleCap),
			"on_disk_not_indexed_count": len(notIndexed),
			"indexed_not_walked_count":  len(ghost),
		},
	}
}

func truncateExamples(ss []string, n int) []string {
	if len(ss) <= n {
		return ss
	}
	return ss[:n]
}
