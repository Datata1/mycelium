package pipeline

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/document"
	"github.com/datata1/mycelium/internal/repo"
	goresolver "github.com/datata1/mycelium/internal/resolver/golang"
)

// Resolver is any per-language ref resolver. v1.2 introduced the Go type
// resolver; v1.3 adds TypeScript and Python scope walkers. Each takes a
// ParseResult and rewrites its References in place, stamping every
// visited call with its own ResolverVersion so SQL-level fallbacks know to
// skip them.
type Resolver interface {
	ResolveFile(ctx context.Context, absPath string, pr *parser.ParseResult) (resolved, total int)
	Ready() bool
}

// InheritanceEmitter is the optional v2.1 extension: resolvers that compute
// type-inheritance relationships (Go's go/types-driven implements check, and
// in future TS/Python equivalents) implement this to append RefInherit
// edges after the main call-resolution pass. The pipeline calls it via a
// type assertion so adding this capability is non-breaking.
type InheritanceEmitter interface {
	EmitInheritance(absPath string, pr *parser.ParseResult) int
}

// Workspace pairs a per-project walker with the matching projects-table
// row. v1.5 pipelines iterate a slice of these instead of a single
// Walker; the legacy Pipeline.Walker still works when Workspaces is empty.
type Workspace struct {
	ProjectID int64 // 0 = implicit root project; no projects table row
	Walker    *repo.Walker
}

// Pipeline orchestrates walk -> parse -> index. For v0.1 it runs as a one-shot
// over the whole repo; v0.2 wires this same code to the fsnotify watcher.
type Pipeline struct {
	Index    *index.Index
	Registry *parser.Registry
	Walker   *repo.Walker // legacy single-root path; used only when Workspaces is empty
	// Workspaces is the v1.5 multi-project path. When non-empty it
	// replaces Walker entirely — each project is walked with its own
	// root + filters and tagged with its project_id on index write.
	Workspaces []Workspace
	// Documents is the v3.3 parallel surface — i18n JSON, package.json
	// deps, go.mod requires. Nil = documents pass disabled (default,
	// keeps legacy callers unchanged). When set, after the main symbol
	// pass finishes, RunOnce iterates the same file list and dispatches
	// every file no symbol parser claimed through this registry.
	Documents *document.Registry
	// Resolvers is keyed by language ("go", "typescript", "python"). A
	// missing entry means textual resolution only for that language.
	Resolvers map[string]Resolver

	// Deprecated: kept for legacy callers; prefer Resolvers["go"].
	GoResolver *goresolver.Resolver

	// FileProjectFor maps an absolute path to its project_id. Populated at
	// construction for the v1.5 workspace path; nil for single-project use.
	FileProjectFor func(absPath string) int64
}

// Report summarizes a pipeline run.
type Report struct {
	FilesScanned int
	FilesChanged int
	FilesSkipped int
	Symbols      int
	Refs         int
	// Documents counts files where the v3.3 document pass produced
	// changed entries (i18n JSON, package.json deps, go.mod requires).
	// Stays 0 when Pipeline.Documents is nil.
	Documents int
	Duration  time.Duration
	Errors    []error
}

// RunOnce walks, parses, and indexes every file the walker yields.
// Errors per file are collected but do not abort the run.
//
// Parsing is CPU-bound (tree-sitter in particular), so we fan it across a
// worker pool. SQLite writes serialize through a single writer goroutine;
// one transaction per file keeps error isolation on partial failures.
func (p *Pipeline) RunOnce(ctx context.Context) (Report, error) {
	start := time.Now()
	var rep Report

	// Walk each workspace (v1.5) or fall back to the legacy single
	// walker. Files get tagged with their project_id here so the
	// downstream writer stays oblivious to workspace layout.
	var files []repo.File
	switch {
	case len(p.Workspaces) > 0:
		for _, ws := range p.Workspaces {
			batch, err := ws.Walker.Walk()
			if err != nil {
				return rep, fmt.Errorf("walk project %d: %w", ws.ProjectID, err)
			}
			for i := range batch {
				batch[i].ProjectID = ws.ProjectID
			}
			files = append(files, batch...)
		}
	case p.Walker != nil:
		batch, err := p.Walker.Walk()
		if err != nil {
			return rep, fmt.Errorf("walk: %w", err)
		}
		files = batch
	default:
		return rep, fmt.Errorf("pipeline: neither Workspaces nor Walker configured")
	}
	rep.FilesScanned = len(files)

	// Small repos don't benefit from parallelism: the goroutine+channel
	// overhead outweighs the gain when per-file parse time is sub-ms (pure
	// Go via stdlib go/ast) and SQLite serializes writes. Kick in parallel
	// only once there's enough work for it to pay off.
	workers := 1
	if len(files) >= 200 {
		workers = runtime.GOMAXPROCS(0)
		if workers > 8 {
			workers = 8 // diminishing returns past 8 for most repos
		}
	}
	if workers < 1 {
		workers = 1
	}

	type job struct {
		File   repo.File
		Parser parser.Parser
	}
	type parsed struct {
		File   repo.File
		Parser parser.Parser
		Result parser.ParseResult
		Source []byte
		Err    error
	}

	jobs := make(chan job, workers*2)
	results := make(chan parsed, workers*2)

	// Feeder: filter out unsupported files and push the rest to workers.
	go func() {
		defer close(jobs)
		for _, f := range files {
			prs := p.Registry.ForPath(f.RelPath)
			if prs == nil {
				results <- parsed{File: f, Err: errSkipped}
				continue
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- job{File: f, Parser: prs}:
			}
		}
	}()

	// Parser workers.
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				content, err := os.ReadFile(j.File.AbsPath)
				if err != nil {
					results <- parsed{File: j.File, Parser: j.Parser, Err: err}
					continue
				}
				res, err := j.Parser.Parse(ctx, j.File.RelPath, content)
				results <- parsed{
					File:   j.File,
					Parser: j.Parser,
					Result: res,
					Source: content,
					Err:    err,
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	// Writer: serial DB writes, one transaction per file.
	for r := range results {
		if r.Err == errSkipped {
			rep.FilesSkipped++
			continue
		}
		if r.Err != nil {
			rep.Errors = append(rep.Errors, fmt.Errorf("%s: %w", r.File.RelPath, r.Err))
			continue
		}
		changed, syms, refs, err := p.writeParsed(ctx, r.File, r.Parser, r.Result, r.Source)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Errorf("%s: %w", r.File.RelPath, err))
			continue
		}
		if changed {
			rep.FilesChanged++
		}
		rep.Symbols += syms
		rep.Refs += refs
	}

	// v3.3 document pass: walk each workspace again with a document-
	// specific include set (the symbol-side Include is language-scoped
	// and doesn't usually let .json / go.mod through). The same
	// excludes apply, so node_modules / dist / .git stay skipped.
	if p.Documents != nil {
		docs, docErrs := p.runDocuments(ctx)
		rep.Documents = docs
		rep.Errors = append(rep.Errors, docErrs...)
	}

	rep.Duration = time.Since(start)
	return rep, nil
}

// documentWalkIncludes are the glob patterns the v3.3 document pass
// uses. Kept here (not in each parser's Supports) because we need
// them at walk time, before any parser sees the file. New document
// kinds in future versions extend this list.
var documentWalkIncludes = []string{
	"**/*.json", // i18n locale files; also picks up package.json
	"**/go.mod",
}

// runDocuments walks every workspace root with documentWalkIncludes,
// then dispatches each matched file to the document registry. Files
// also claimed by a symbol parser are skipped (symbol parsers own
// the files row's language column).
func (p *Pipeline) runDocuments(ctx context.Context) (int, []error) {
	var count int
	var errs []error
	var documents []repo.File

	switch {
	case len(p.Workspaces) > 0:
		for _, ws := range p.Workspaces {
			dw := repo.NewWalker(ws.Walker.Root, documentWalkIncludes, ws.Walker.Exclude, ws.Walker.MaxFileSizeKB)
			batch, err := dw.Walk()
			if err != nil {
				errs = append(errs, fmt.Errorf("walk documents project=%d: %w", ws.ProjectID, err))
				continue
			}
			for i := range batch {
				batch[i].ProjectID = ws.ProjectID
			}
			documents = append(documents, batch...)
		}
	case p.Walker != nil:
		dw := repo.NewWalker(p.Walker.Root, documentWalkIncludes, p.Walker.Exclude, p.Walker.MaxFileSizeKB)
		batch, err := dw.Walk()
		if err != nil {
			return 0, []error{fmt.Errorf("walk documents: %w", err)}
		}
		documents = batch
	}

	for _, f := range documents {
		if p.Registry != nil && p.Registry.ForPath(f.RelPath) != nil {
			continue
		}
		dp := p.Documents.ForPath(f.RelPath)
		if dp == nil {
			continue
		}
		ch, err := p.processDocumentFile(ctx, dp, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.RelPath, err))
			continue
		}
		if ch {
			count++
		}
	}
	return count, errs
}

// processDocumentFile reads + parses one document file and writes
// its entries. Mirrors processFile (symbol path) but routes through
// UpsertDocumentFile + ReplaceFileDocumentEntries.
func (p *Pipeline) processDocumentFile(ctx context.Context, dp parser.DocumentParser, f repo.File) (bool, error) {
	content, err := os.ReadFile(f.AbsPath)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}
	res, err := dp.Parse(ctx, f.RelPath, content)
	if err != nil {
		return false, fmt.Errorf("parse document: %w", err)
	}
	db := p.Index.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	upsert, err := p.Index.UpsertDocumentFile(
		ctx, tx, f.RelPath, dp.Kind(),
		int64(len(content)), f.MTimeNS,
		res.ContentHash, res.ContentHash, f.ProjectID,
	)
	if err != nil {
		return false, err
	}
	if !upsert.Changed {
		return false, tx.Commit()
	}
	if err := p.Index.ReplaceFileDocumentEntries(ctx, tx, upsert.FileID, dp.Kind(), res.Entries); err != nil {
		return true, err
	}
	if err := tx.Commit(); err != nil {
		return true, err
	}
	return true, nil
}

// errSkipped is an internal signal that a file had no parser; it never
// leaves the RunOnce goroutine tree.
var errSkipped = fmt.Errorf("skipped (no parser)")

// HandleChange processes a single file change from the watcher. The relative
// path is looked up against the registered parsers; if none supports it, the
// call is a no-op (returned changed=false). A removed file is handled by
// deleting its row; cascades drop symbols/refs/chunks.
func (p *Pipeline) HandleChange(ctx context.Context, relPath, absPath string, removed bool) (changed bool, err error) {
	if removed {
		db := p.Index.DB()
		_, err := db.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, relPath)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	prs := p.Registry.ForPath(relPath)
	if prs != nil {
		info, err := os.Stat(absPath)
		if err != nil {
			return false, err
		}
		f := repo.File{AbsPath: absPath, RelPath: relPath, SizeKB: int(info.Size() / 1024), MTimeNS: info.ModTime().UnixNano()}
		if p.FileProjectFor != nil {
			f.ProjectID = p.FileProjectFor(absPath)
		}
		ch, _, _, err := p.processFile(ctx, prs, f)
		return ch, err
	}
	// v3.3 document path: no symbol parser claimed it, try the
	// document registry. Returns silently when neither surface
	// recognises the file.
	if p.Documents != nil {
		if dp := p.Documents.ForPath(relPath); dp != nil {
			info, err := os.Stat(absPath)
			if err != nil {
				return false, err
			}
			f := repo.File{AbsPath: absPath, RelPath: relPath, SizeKB: int(info.Size() / 1024), MTimeNS: info.ModTime().UnixNano()}
			if p.FileProjectFor != nil {
				f.ProjectID = p.FileProjectFor(absPath)
			}
			return p.processDocumentFile(ctx, dp, f)
		}
	}
	return false, nil
}

func (p *Pipeline) processFile(ctx context.Context, prs parser.Parser, f repo.File) (bool, int, int, error) {
	content, err := os.ReadFile(f.AbsPath)
	if err != nil {
		return false, 0, 0, fmt.Errorf("read: %w", err)
	}
	result, err := prs.Parse(ctx, f.RelPath, content)
	if err != nil {
		return false, 0, 0, fmt.Errorf("parse: %w", err)
	}
	return p.writeParsed(ctx, f, prs, result, content)
}

// writeParsed is the DB-writing half of processFile, used both by single-file
// update paths (watcher, hook) and the parallel RunOnce loop.
func (p *Pipeline) writeParsed(ctx context.Context, f repo.File, prs parser.Parser, result parser.ParseResult, content []byte) (bool, int, int, error) {
	// v1.2+: language-specific resolver rewrites call DstNames and stamps
	// ResolverVersion before refs hit the DB. No-op when no resolver is
	// registered for this language or when the resolver isn't ready.
	if res := p.resolverFor(prs.Language()); res != nil && res.Ready() {
		res.ResolveFile(ctx, f.AbsPath, &result)
		// v2.1: resolvers that implement InheritanceEmitter additionally
		// emit RefInherit edges (concrete -> interface) so the query
		// layer can fan out through interfaces (Chinthareddy 2026 §6.4).
		if ie, ok := res.(InheritanceEmitter); ok {
			ie.EmitInheritance(f.AbsPath, &result)
		}
	}

	db := p.Index.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	upsert, err := p.Index.UpsertFile(ctx, tx, f.RelPath, prs.Language(), int64(len(content)), f.MTimeNS, result.ContentHash, result.ParseHash, f.ProjectID)
	if err != nil {
		return false, 0, 0, err
	}
	if !upsert.Changed {
		if err := tx.Commit(); err != nil {
			return false, 0, 0, err
		}
		return false, 0, 0, nil
	}

	symIDs, err := p.Index.ReplaceFileSymbols(ctx, tx, upsert.FileID, result.Symbols)
	if err != nil {
		return true, 0, 0, err
	}
	if err := p.Index.ReplaceFileRefs(ctx, tx, upsert.FileID, symIDs, result.References); err != nil {
		return true, len(result.Symbols), 0, err
	}
	if _, err := p.Index.SyncResolvedFlag(ctx, tx); err != nil {
		return true, len(result.Symbols), len(result.References), err
	}
	if _, err := p.Index.ResolveRefs(ctx, tx); err != nil {
		return true, len(result.Symbols), len(result.References), err
	}

	if err := tx.Commit(); err != nil {
		return true, len(result.Symbols), len(result.References), err
	}
	return true, len(result.Symbols), len(result.References), nil
}

// resolverFor returns the resolver registered for lang, falling back to the
// legacy GoResolver field so pre-v1.3 construction keeps working unchanged.
func (p *Pipeline) resolverFor(lang string) Resolver {
	if r, ok := p.Resolvers[lang]; ok {
		return r
	}
	if lang == "go" && p.GoResolver != nil {
		return p.GoResolver
	}
	return nil
}
