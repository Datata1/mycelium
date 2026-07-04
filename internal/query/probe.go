package query

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datata1/mycelium/internal/repo"
)

// FSProbe explains why a path is absent from the index by checking the
// filesystem against the indexing configuration. It exists so empty
// query results can distinguish "no such thing" from "excluded by
// config" from "index is stale" — the ambiguity that silently sends
// agents back to grep.
type FSProbe struct {
	Root          string   // absolute repo root
	Include       []string // top-level include globs
	Exclude       []string // top-level exclude globs
	MaxFileSizeKB int
}

// DiagnosePath returns 0..N hint lines for a repo-relative path that
// produced no index rows. Order: hard facts first (not on disk),
// config explanations next, staleness last (lastFullScan from
// index_meta; zero = never reconciled).
func (p *FSProbe) DiagnosePath(rel string, lastFullScan time.Time) []string {
	if p == nil || p.Root == "" || rel == "" {
		return nil
	}
	rel = filepath.ToSlash(rel)
	info, err := os.Stat(filepath.Join(p.Root, filepath.FromSlash(rel)))
	if err != nil {
		return []string{fmt.Sprintf("%s is not on disk either (deleted, renamed, or never existed)", rel)}
	}
	if info.IsDir() {
		return nil
	}
	for _, pat := range p.Exclude {
		if repo.DoublestarMatch(pat, rel) {
			return []string{fmt.Sprintf(
				"%s matches exclude pattern %q — excluded from indexing by config", rel, pat)}
		}
	}
	if len(p.Include) > 0 {
		matched := false
		for _, pat := range p.Include {
			if repo.DoublestarMatch(pat, rel) {
				matched = true
				break
			}
		}
		if !matched {
			return []string{fmt.Sprintf(
				"%s does not match any include glob (%s) — its extension/location is not configured for indexing",
				rel, strings.Join(p.Include, ", "))}
		}
	}
	if p.MaxFileSizeKB > 0 && info.Size() > int64(p.MaxFileSizeKB)*1024 {
		return []string{fmt.Sprintf(
			"%s is %d KB — over the max_file_size_kb=%d cap, so it is not indexed",
			rel, info.Size()/1024, p.MaxFileSizeKB)}
	}
	// On disk, allowed by config, yet not indexed: the index is stale.
	scan := "no full reconcile recorded"
	if !lastFullScan.IsZero() {
		scan = fmt.Sprintf("file mtime %s, last full scan %s",
			info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			lastFullScan.UTC().Format("2006-01-02T15:04:05Z"))
	}
	return []string{fmt.Sprintf(
		"%s exists on disk but is not indexed — the index is stale (%s); is the daemon running? `myco index` reconciles",
		rel, scan)}
}

// SetProbe attaches a filesystem probe so empty results and not-found
// errors can explain WHY a path is missing. Nil (the default) keeps
// probe-free behavior.
func (r *Reader) SetProbe(p *FSProbe) { r.probe = p }

// diagnosePath is the Reader-side wrapper: pairs the probe with the
// reconcile timestamp. Returns nil when no probe is attached.
func (r *Reader) diagnosePath(ctx context.Context, rel string) []string {
	if r.probe == nil {
		return nil
	}
	last, _, _ := r.LastFullScanAt(ctx)
	return r.probe.DiagnosePath(rel, last)
}

// joinDiagnosis renders diagnosis lines as an error-message tail;
// "" when there is nothing to say, so callers concatenate blindly.
func joinDiagnosis(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return "\n" + strings.Join(lines, "\n")
}
