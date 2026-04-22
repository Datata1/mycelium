package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/jdwiederstein/mycelium/internal/chunker"
	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/repo"
	goresolver "github.com/jdwiederstein/mycelium/internal/resolver/golang"
)

// Pipeline orchestrates walk -> parse -> index. For v0.1 it runs as a one-shot
// over the whole repo; v0.2 wires this same code to the fsnotify watcher.
type Pipeline struct {
	Index    *index.Index
	Registry *parser.Registry
	Walker   *repo.Walker
	Embedder embed.Embedder // Noop when the user hasn't configured a provider
	// GoResolver is optional; when non-nil and Ready, Go refs are resolved
	// with type info (v1.2 Pillar A). Nil resolver or Ready()==false means
	// refs stay as textual output — the short-name SQL fallback still runs.
	GoResolver *goresolver.Resolver
	ChunkerOpts chunker.Options
	Logger   Logger
}

// Logger is a minimal dependency so callers can plug in whatever they have.
type Logger interface {
	Printf(format string, args ...any)
}

type stdoutLogger struct{ w io.Writer }

func (l stdoutLogger) Printf(format string, args ...any) {
	fmt.Fprintf(l.w, format, args...)
}

// NewStdoutLogger is a convenience for the `myco index` command.
func NewStdoutLogger() Logger { return stdoutLogger{w: os.Stdout} }

// Report summarizes a pipeline run.
type Report struct {
	FilesScanned int
	FilesChanged int
	FilesSkipped int
	Symbols      int
	Refs         int
	Duration     time.Duration
	Errors       []error
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

	files, err := p.Walker.Walk()
	if err != nil {
		return rep, fmt.Errorf("walk: %w", err)
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
	rep.Duration = time.Since(start)
	return rep, nil
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
	if prs == nil {
		return false, nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return false, err
	}
	f := repo.File{AbsPath: absPath, RelPath: relPath, SizeKB: int(info.Size() / 1024), MTimeNS: info.ModTime().UnixNano()}
	ch, _, _, err := p.processFile(ctx, prs, f)
	return ch, err
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
	// v1.2: if a Go type resolver is wired up, let it rewrite call DstNames
	// to fully-qualified form before the refs hit the DB. No-op for other
	// languages or when the resolver failed to load the module.
	if prs.Language() == "go" && p.GoResolver != nil && p.GoResolver.Ready() {
		p.GoResolver.ResolveFile(f.AbsPath, &result)
	}

	db := p.Index.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	upsert, err := p.Index.UpsertFile(ctx, tx, f.RelPath, prs.Language(), int64(len(content)), f.MTimeNS, result.ContentHash, result.ParseHash)
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

	// Chunking + embed queue. Skips quietly when the embedder is Noop.
	chunks := chunker.FromSymbols(content, result.Symbols, p.ChunkerOpts)
	embedderModel := "none"
	if p.Embedder != nil {
		embedderModel = p.Embedder.Model()
	}
	if _, err := p.Index.ReplaceFileChunks(ctx, tx, upsert.FileID, symIDs, chunks, embedderModel); err != nil {
		return true, len(result.Symbols), len(result.References), err
	}

	if err := tx.Commit(); err != nil {
		return true, len(result.Symbols), len(result.References), err
	}
	return true, len(result.Symbols), len(result.References), nil
}

// NullLogger discards all log output.
type NullLogger struct{}

func (NullLogger) Printf(string, ...any) {}

// Ensure the sql package import is referenced so removing unused imports doesn't
// silently drop the dep. The pipeline touches database transactions indirectly
// through index.*; this keeps future refactoring honest.
var _ = (*sql.Tx)(nil)
