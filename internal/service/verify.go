package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/datata1/mycelium/internal/check"
	"github.com/datata1/mycelium/internal/gitref"
	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/query"
)

// verifySinceDefault makes a bare `myco check` verify exactly the work
// in progress: everything different from HEAD, including uncommitted
// tracked edits.
const verifySinceDefault = "HEAD"

// maxDetailExamples bounds example lists inside check details so a
// 400-file diff doesn't produce a 400-line report.
const maxDetailExamples = 5

// VerifyChanges is the structural verifier behind `myco check` and the
// verify_changes tool. It never writes to the index; the daemon
// dispatcher pre-gates a reconcile when changed paths are stale (see
// StalePathsFor).
func (s *Service) VerifyChanges(ctx context.Context, p ipc.VerifyChangesParams) (ipc.VerifyReport, error) {
	ref := p.Since
	if ref == "" {
		ref = verifySinceDefault
	}
	rep := ipc.VerifyReport{Since: ref, Checks: []ipc.VerifyCheck{}}

	base, changed, err := gitref.ChangedSince(ctx, s.root, ref)
	if err != nil {
		return rep, err
	}
	rep.Base = base
	rep.ChangedFiles = len(changed)

	add := func(c ipc.VerifyCheck) {
		rep.Checks = append(rep.Checks, c)
		switch c.Level {
		case string(check.LevelFail):
			rep.Summary.Fail++
		case string(check.LevelWarn):
			rep.Summary.Warn++
		default:
			rep.Summary.Pass++
		}
	}

	if len(changed) == 0 {
		add(ipc.VerifyCheck{Name: "git_scope", Level: "pass",
			Message: "clean tree — nothing to verify"})
		return rep, nil
	}
	if len(changed) > query.MaxPathsIn {
		return rep, fmt.Errorf("verify scope is %d changed paths (max %d) — pick a tighter base ref", len(changed), query.MaxPathsIn)
	}
	add(ipc.VerifyCheck{Name: "git_scope", Level: "pass",
		Message: fmt.Sprintf("%d changed file(s) vs base %s", len(changed), shortSHA(base))})

	// Freshness gate: a verdict from a stale index is worse than none.
	// Daemon-up, the dispatcher has already reconciled; this fail path
	// is the daemon-down story.
	stale, err := s.staleChanged(ctx, changed)
	if err != nil {
		return rep, err
	}
	if len(stale) > 0 {
		add(ipc.VerifyCheck{Name: "index_fresh_for_changes", Level: "fail",
			Message: fmt.Sprintf("index is stale for %d changed file(s) — start `myco daemon` or run `myco index`, then re-check", len(stale)),
			Detail:  map[string]any{"examples": truncStrings(stale, maxDetailExamples)}})
	} else {
		add(ipc.VerifyCheck{Name: "index_fresh_for_changes", Level: "pass",
			Message: "index is current for all changed files"})
	}

	if s.parsers == nil {
		add(ipc.VerifyCheck{Name: "parse_old_versions", Level: "warn",
			Message: "no parser registry attached — removed-symbol detection skipped"})
		return rep, nil
	}

	old, parsedCount, issues := s.oldSymbols(ctx, base, changed)
	if len(issues) > 0 {
		add(ipc.VerifyCheck{Name: "parse_old_versions", Level: "warn",
			Message: fmt.Sprintf("%d of %d old file version(s) could not be parsed — their removed symbols are invisible to this check", len(issues), parsedCount+len(issues)),
			Detail:  map[string]any{"examples": truncStrings(issues, maxDetailExamples)}})
	} else {
		add(ipc.VerifyCheck{Name: "parse_old_versions", Level: "pass",
			Message: fmt.Sprintf("%d old file version(s) parsed", parsedCount)})
	}

	quals := make([]string, 0, len(old))
	for _, o := range old {
		quals = append(quals, o.Qualified)
	}
	exists, err := s.reader.QualifiedExist(ctx, quals)
	if err != nil {
		return rep, err
	}
	removed := check.Diff(old, exists)

	var classified []check.ClassifiedRemoved
	level := check.LevelPass
	if len(removed) > 0 {
		names := make([]query.RemovedName, len(removed))
		for i, rm := range removed {
			names[i] = query.RemovedName{Qualified: rm.Qualified, Name: rm.Name}
		}
		danglers, err := s.reader.DanglingRefs(ctx, names, changed)
		if err != nil {
			return rep, err
		}
		classified, level = check.Classify(removed, danglers)
	}

	danglingSymbols := 0
	for _, c := range classified {
		dto := ipc.RemovedSymbol{Qualified: c.Qualified, Kind: c.Kind, OldPath: c.OldPath}
		for _, d := range c.Danglers {
			dto.Danglers = append(dto.Danglers, ipc.VerifyDangler{
				Path: d.SrcPath, Line: d.Line, Kind: d.Kind, SrcSymbol: d.SrcQualified, Exact: d.Exact,
			})
		}
		if len(dto.Danglers) > 0 {
			danglingSymbols++
		}
		rep.Removed = append(rep.Removed, dto)
	}

	var msg string
	switch {
	case level == check.LevelFail:
		msg = fmt.Sprintf("%d removed symbol(s) still referenced from outside the change set — fix the call sites or restore the symbol(s)", danglingSymbols)
	case level == check.LevelWarn:
		msg = fmt.Sprintf("%d removed symbol(s) possibly still referenced (short-name matches only) — check the listed call sites", danglingSymbols)
	case len(removed) > 0:
		msg = fmt.Sprintf("%d symbol(s) removed cleanly — no references from outside the change set", len(removed))
	default:
		msg = "no symbols removed"
	}
	add(ipc.VerifyCheck{Name: "removed_but_referenced", Level: string(level), Message: msg})

	return rep, nil
}

// StalePathsFor lets the daemon dispatcher decide whether to reconcile
// before answering verify_changes: it returns the changed paths whose
// index rows are out of date. Errors degrade to "nothing stale" — the
// verifier itself will surface them properly.
func (s *Service) StalePathsFor(ctx context.Context, ref string) []string {
	if ref == "" {
		ref = verifySinceDefault
	}
	_, changed, err := gitref.ChangedSince(ctx, s.root, ref)
	if err != nil || len(changed) == 0 || len(changed) > query.MaxPathsIn {
		return nil
	}
	stale, err := s.staleChanged(ctx, changed)
	if err != nil {
		return nil
	}
	return stale
}

// staleChanged compares the index rows of the changed paths against the
// working tree: modified since indexing, indexed-but-deleted, or
// parseable-on-disk-but-unindexed all count as stale.
func (s *Service) staleChanged(ctx context.Context, changed []string) ([]string, error) {
	rows, err := s.reader.FilesFreshness(ctx, changed)
	if err != nil {
		return nil, err
	}
	indexed := map[string]query.FileFreshnessRow{}
	for _, row := range rows {
		display := row.Path
		if row.ProjectRoot != "" {
			display = row.ProjectRoot + "/" + row.Path
		}
		indexed[display] = row
	}

	var stale []string
	for _, path := range changed {
		abs := filepath.Join(s.root, path)
		fi, statErr := os.Stat(abs)
		row, inIndex := indexed[path]
		switch {
		case inIndex && statErr != nil:
			stale = append(stale, path+" (indexed but missing on disk)")
		case inIndex && fi.ModTime().UnixNano() > row.MTimeNS:
			stale = append(stale, path+" (modified since indexing)")
		case !inIndex && statErr == nil && s.parsers != nil && s.parsers.ForPath(path) != nil:
			stale = append(stale, path+" (on disk but not indexed)")
		}
	}
	return stale, nil
}

// oldSymbols extracts symbols from the base-commit versions of the
// changed files. Files without a registered parser are skipped; files
// absent at base (newly added) contribute nothing.
func (s *Service) oldSymbols(ctx context.Context, base string, changed []string) (old []check.OldSymbol, parsed int, issues []string) {
	for _, path := range changed {
		prs := s.parsers.ForPath(path)
		if prs == nil {
			continue
		}
		content, ok, err := gitref.ShowAtCommit(ctx, s.root, base, path)
		if err != nil {
			issues = append(issues, path+": "+err.Error())
			continue
		}
		if !ok {
			continue // newly added — no old symbols
		}
		res, err := prs.Parse(ctx, path, content)
		if err != nil {
			issues = append(issues, path+": "+err.Error())
			continue
		}
		parsed++
		for _, sym := range res.Symbols {
			old = append(old, check.OldSymbol{
				Qualified: sym.Qualified, Name: sym.Name, Kind: string(sym.Kind), Path: path,
			})
		}
	}
	return old, parsed, issues
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func truncStrings(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	out := append([]string{}, in[:n]...)
	return append(out, fmt.Sprintf("… and %d more", len(in)-n))
}

// SelectTests maps the change set onto the test files that exercise it:
// changed files → symbols defined there → multi-seed inbound closure →
// files matching test-naming conventions. Changed test files themselves
// rank at distance 0.
func (s *Service) SelectTests(ctx context.Context, p ipc.SelectTestsParams) (ipc.SelectTestsResult, error) {
	ref := p.Since
	if ref == "" {
		ref = verifySinceDefault
	}
	res := ipc.SelectTestsResult{TestFiles: []ipc.TestFileHit{}}

	_, changed, err := gitref.ChangedSince(ctx, s.root, ref)
	if err != nil {
		return res, err
	}
	res.ChangedFiles = len(changed)
	if len(changed) == 0 {
		res.Notes = append(res.Notes, "clean tree — nothing changed, no tests selected")
		return res, nil
	}
	if len(changed) > query.MaxPathsIn {
		return res, fmt.Errorf("test-selection scope is %d changed paths (max %d) — pick a tighter base ref", len(changed), query.MaxPathsIn)
	}

	seen := map[string]struct{}{}
	// Changed test files are always in the selection, at distance 0.
	for _, path := range changed {
		if check.IsTestFile(path) {
			res.TestFiles = append(res.TestFiles, ipc.TestFileHit{Path: path, Distance: 0})
			seen[path] = struct{}{}
		}
	}

	syms, err := s.reader.SymbolsInFiles(ctx, changed)
	if err != nil {
		return res, err
	}
	seeds := make([]int64, len(syms))
	for i, sym := range syms {
		seeds[i] = sym.ID
	}
	res.Seeds = len(seeds)
	if len(seeds) == 0 {
		res.Notes = append(res.Notes, "no indexed symbols in the changed files — is the index fresh? (run `myco check`)")
		return res, nil
	}

	hits, err := s.reader.InboundClosureFiles(ctx, seeds, p.Depth)
	if err != nil {
		return res, err
	}
	for _, h := range hits {
		if p.Project != "" && h.Project != p.Project {
			continue
		}
		if !check.IsTestFile(h.Path) {
			continue
		}
		if _, dup := seen[h.Path]; dup {
			continue
		}
		seen[h.Path] = struct{}{}
		res.TestFiles = append(res.TestFiles, ipc.TestFileHit{Path: h.Path, Project: h.Project, Distance: h.Distance})
	}
	if len(res.TestFiles) == 0 {
		res.Notes = append(res.Notes, "no test files reach the changed code — consider adding coverage before large edits")
	}
	return res, nil
}
