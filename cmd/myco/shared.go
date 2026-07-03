package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/parser/document"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/repo"
)

// openIndex opens the repo's SQLite index, creating parent directories when
// the index lives outside the repo (e.g. a ~/... path from `myco init --user`).
func openIndex(rc repoCtx) (*index.Index, error) {
	p := rc.AbsIndexPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir index dir: %w", err)
	}
	return index.Open(p)
}

// buildWorkspaces materializes the v1.5 per-project walkers from config
// and upserts the matching projects-table rows. Returns nil on a single-
// project config (no projects: list); the pipeline falls back to its
// single-Walker path automatically. Projects absent from the current
// config are pruned from the DB.
func buildWorkspaces(ctx context.Context, rc repoCtx, ix *index.Index) ([]pipeline.Workspace, func(string) int64, error) {
	if len(rc.Cfg.Projects) == 0 {
		return nil, nil, nil
	}
	var (
		workspaces []pipeline.Workspace
		keep       []int64
		// prefixes feeds the watcher-path prefix resolver. Sorted by
		// descending length so longest-prefix wins.
		prefixes []struct {
			abs string
			id  int64
		}
	)
	for _, pc := range rc.Cfg.Projects {
		id, err := ix.UpsertProject(ctx, pc.Name, pc.Root)
		if err != nil {
			return nil, nil, fmt.Errorf("project %s: %w", pc.Name, err)
		}
		keep = append(keep, id)
		absRoot := rc.Root + "/" + pc.Root
		include := pc.Include
		if len(include) == 0 {
			include = rc.Cfg.Include
		}
		exclude := pc.Exclude
		if len(exclude) == 0 {
			exclude = rc.Cfg.Exclude
		}
		w := repo.NewWalker(absRoot, include, exclude, rc.Cfg.Index.MaxFileSizeKB)
		workspaces = append(workspaces, pipeline.Workspace{ProjectID: id, Walker: w})
		prefixes = append(prefixes, struct {
			abs string
			id  int64
		}{abs: absRoot, id: id})
	}
	if err := ix.PruneProjects(ctx, keep); err != nil {
		return nil, nil, fmt.Errorf("prune projects: %w", err)
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i].abs) > len(prefixes[j].abs) })
	resolver := func(abs string) int64 {
		for _, p := range prefixes {
			if len(abs) >= len(p.abs) && abs[:len(p.abs)] == p.abs &&
				(len(abs) == len(p.abs) || abs[len(p.abs)] == '/') {
				return p.id
			}
		}
		return 0
	}
	return workspaces, resolver, nil
}

// buildDocumentRegistry assembles the v3.3 document parsers. The three
// kinds (i18n_json, package_json_deps, go_mod_requires) are always
// registered — they only fire when the walker encounters matching files,
// so a code-only repo pays nothing for them.
func buildDocumentRegistry() *document.Registry {
	r := document.NewRegistry()
	r.Register(document.NewI18NJSON())
	r.Register(document.NewPackageJSON())
	r.Register(document.NewGoMod())
	return r
}

// truncate shortens s to at most max bytes, appending "..." when cut.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
