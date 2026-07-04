// Package service owns read-path execution: one typed method per query
// tool, taking ipc params and returning ipc DTOs. The daemon dispatcher
// and the CLI's daemon-down fallback both call into it, so the two paths
// cannot drift (plans/refac/04). It is read-only by construction — no
// pipeline handle — which keeps the daemon the sole SQLite writer.
package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/datata1/mycelium/internal/gitref"
	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/query"
)

// Service executes read requests against the index. It is the only
// component outside the write path that constructs a query.Reader.
type Service struct {
	reader *query.Reader
	root   string // absolute repo root; lexical search + --since need it
	log    *slog.Logger
}

// NewReadOnly builds a Service over an already-open index. The caller
// retains ownership of ix. log may be nil.
func NewReadOnly(ix *index.Index, repoRoot string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{reader: query.NewReader(ix.DB()), root: repoRoot, log: log}
}

// SetProbe attaches a filesystem probe (built from the indexing config)
// so empty results and not-found errors explain WHY a path is missing —
// excluded, wrong extension, oversize, or stale index. Optional; without
// it, misses stay explanation-free.
func (s *Service) SetProbe(p *query.FSProbe) { s.reader.SetProbe(p) }

// resolveSince turns the optional git-ref string into a resolved path
// list. Empty ref -> nil (unscoped). Git errors surface to the caller
// rather than silently becoming an empty filter.
func (s *Service) resolveSince(ctx context.Context, ref string) ([]string, error) {
	if ref == "" {
		return nil, nil
	}
	return gitref.ResolveSince(ctx, s.root, ref)
}

func (s *Service) FindSymbol(ctx context.Context, p ipc.FindSymbolParams) (ipc.FindSymbolResult, error) {
	paths, err := s.resolveSince(ctx, p.Since)
	if err != nil {
		return ipc.FindSymbolResult{}, err
	}
	return s.reader.FindSymbol(ctx, p.Name, p.Kind, p.Project, p.Limit, paths, p.Focus)
}

func (s *Service) GetReferences(ctx context.Context, p ipc.GetReferencesParams) (ipc.GetReferencesResult, error) {
	paths, err := s.resolveSince(ctx, p.Since)
	if err != nil {
		return ipc.GetReferencesResult{}, err
	}
	return s.reader.GetReferences(ctx, p.Target, p.Project, p.Limit, paths)
}

func (s *Service) ListFiles(ctx context.Context, p ipc.ListFilesParams) ([]ipc.FileHit, error) {
	paths, err := s.resolveSince(ctx, p.Since)
	if err != nil {
		return nil, err
	}
	return s.reader.ListFiles(ctx, p.Language, p.NameContains, p.Project, p.Limit, paths)
}

func (s *Service) GetFileOutline(ctx context.Context, p ipc.GetFileOutlineParams) ([]ipc.FileOutlineItem, error) {
	return s.reader.GetFileOutline(ctx, p.Path, p.Focus)
}

func (s *Service) GetFileSummary(ctx context.Context, p ipc.GetFileSummaryParams) (ipc.FileSummary, error) {
	return s.reader.GetFileSummary(ctx, p.Path)
}

func (s *Service) GetNeighborhood(ctx context.Context, p ipc.GetNeighborhoodParams) (ipc.Neighborhood, error) {
	dir, err := query.ParseDirection(p.Direction)
	if err != nil {
		return ipc.Neighborhood{}, fmt.Errorf("%w: %v", ipc.ErrBadParams, err)
	}
	return s.reader.GetNeighborhood(ctx, p.Target, p.Project, p.Depth, dir, p.Focus)
}

func (s *Service) SearchLexical(ctx context.Context, p ipc.SearchLexicalParams) (ipc.SearchLexicalResult, error) {
	paths, err := s.resolveSince(ctx, p.Since)
	if err != nil {
		return ipc.SearchLexicalResult{}, err
	}
	return s.reader.SearchLexical(ctx, p.Pattern, p.PathContains, p.Project, p.K, s.root, paths)
}

func (s *Service) Stats(ctx context.Context) (ipc.Stats, error) {
	return s.reader.Stats(ctx)
}

func (s *Service) ImpactAnalysis(ctx context.Context, p ipc.ImpactAnalysisParams) (ipc.Impact, error) {
	paths, err := s.resolveSince(ctx, p.Since)
	if err != nil {
		return ipc.Impact{}, err
	}
	return s.reader.ImpactAnalysis(ctx, p.Target, p.Kind, p.Project, p.Depth, paths)
}

func (s *Service) CriticalPath(ctx context.Context, p ipc.CriticalPathParams) (ipc.CriticalPathResult, error) {
	return s.reader.CriticalPath(ctx, p.From, p.To, p.Project, p.Depth, p.K)
}

func (s *Service) ReadFocused(ctx context.Context, p ipc.ReadFocusedParams) (ipc.FocusedRead, error) {
	return s.reader.ReadFocused(ctx, s.root, p.Path, p.Focus)
}

func (s *Service) FindDocumentKey(ctx context.Context, p ipc.FindDocumentKeyParams) ([]ipc.DocumentHit, error) {
	return s.reader.FindDocumentKey(ctx, p.Key, p.Kind, p.Project, p.Limit)
}

// Reader exposes the underlying query.Reader for callers that need
// read methods outside the wire surface (doctor, stats aggregation).
// It stays read-only; nothing here can reach the write path.
func (s *Service) Reader() *query.Reader { return s.reader }
