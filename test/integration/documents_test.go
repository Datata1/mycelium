// Integration test for v3.3 documents surface.
// Indexes an i18n JSON fixture end-to-end via the document pass:
// pipeline registers the file in `files` with document_kind set,
// flattens the JSON tree into (key, value, line) entries in the
// `documents` table, and skips re-indexing on unchanged content.
package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/document"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
)

func TestIntegration_DocumentsI18NJSON(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, "testdata/fixtures/documents")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	defer ix.Close()

	// Empty symbol registry — this fixture is pure documents.
	symReg := parser.NewRegistry()

	docReg := document.NewRegistry()
	docReg.Register(document.NewI18NJSON())
	docReg.Register(document.NewPackageJSON())
	docReg.Register(document.NewGoMod())

	walker := repo.NewWalker(dst, []string{"**/*.go"}, nil, 0) // include patterns intentionally NOT matching the JSON; documentWalkIncludes handles it.
	p := &pipeline.Pipeline{
		Index:     ix,
		Registry:  symReg,
		Walker:    walker,
		Documents: docReg,
	}
	rep, err := p.RunOnce(ctx)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if rep.Documents == 0 {
		t.Fatalf("expected at least one document changed; got %d (errors: %v)", rep.Documents, rep.Errors)
	}

	t.Run("file_registered_with_document_kind", func(t *testing.T) {
		var kind string
		err := ix.DB().QueryRowContext(ctx,
			`SELECT document_kind FROM files WHERE path = ?`,
			"locales/en.json").Scan(&kind)
		if err != nil {
			t.Fatalf("select file: %v", err)
		}
		if kind != "i18n_json" {
			t.Errorf("document_kind: got %q, want %q", kind, "i18n_json")
		}
	})

	t.Run("flattened_keys_indexed", func(t *testing.T) {
		want := map[string]string{
			"topbar.navigation.back":       "Go back",
			"topbar.navigation.workspaces": "Go back to team workspaces",
			"topbar.navigation.items.0":    "one",
			"topbar.navigation.items.1":    "two",
			"topbar.profile.settings":      "Settings",
			"topbar.profile.logout":        "Sign out",
			"errors.notFound":              "Page not found",
		}
		rows, err := ix.DB().QueryContext(ctx,
			`SELECT key, value FROM documents WHERE kind = 'i18n_json'`)
		if err != nil {
			t.Fatalf("select documents: %v", err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got[k] = v
		}
		if len(got) != len(want) {
			t.Errorf("entry count: got %d, want %d  (got=%v)", len(got), len(want), got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("entry %q: got %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("line_numbers_within_file", func(t *testing.T) {
		// The fixture spans ~17 lines. Every entry's line should be
		// within the file bounds and non-zero.
		rows, err := ix.DB().QueryContext(ctx,
			`SELECT key, line FROM documents WHERE kind = 'i18n_json'`)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var line int
			if err := rows.Scan(&k, &line); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if line < 1 || line > 50 {
				t.Errorf("entry %q: line %d out of fixture bounds", k, line)
			}
		}
	})

	t.Run("re_run_is_idempotent", func(t *testing.T) {
		// Running RunOnce a second time without changing the file
		// should report 0 changed documents (content hash matches).
		rep2, err := p.RunOnce(ctx)
		if err != nil {
			t.Fatalf("rerun: %v", err)
		}
		if rep2.Documents != 0 {
			t.Errorf("second RunOnce changed %d documents; expected 0", rep2.Documents)
		}
	})

	// v3.3 B2: FindDocumentKey query
	reader := query.NewReader(ix.DB())

	t.Run("find_document_key_substring", func(t *testing.T) {
		hits, err := reader.FindDocumentKey(ctx, "navigation", "", "", 10)
		if err != nil {
			t.Fatalf("FindDocumentKey: %v", err)
		}
		// 4 navigation entries: back, workspaces, items.0, items.1
		if len(hits) < 4 {
			t.Errorf("substring 'navigation': got %d hits, want >= 4", len(hits))
		}
		for _, h := range hits {
			if h.Kind != "i18n_json" {
				t.Errorf("unexpected kind %q", h.Kind)
			}
			if h.Path != "locales/en.json" {
				t.Errorf("unexpected path %q", h.Path)
			}
			if h.Line < 1 || h.Line > 50 {
				t.Errorf("line out of bounds: %d (key=%q)", h.Line, h.Key)
			}
		}
	})

	t.Run("find_document_key_exact_match_wins", func(t *testing.T) {
		hits, err := reader.FindDocumentKey(ctx, "errors.notFound", "", "", 10)
		if err != nil {
			t.Fatalf("FindDocumentKey: %v", err)
		}
		if len(hits) == 0 {
			t.Fatalf("exact 'errors.notFound': got 0 hits")
		}
		if hits[0].Key != "errors.notFound" {
			t.Errorf("first hit key: got %q, want %q", hits[0].Key, "errors.notFound")
		}
		if hits[0].Value != "Page not found" {
			t.Errorf("value: got %q, want %q", hits[0].Value, "Page not found")
		}
	})

	t.Run("find_document_key_kind_filter", func(t *testing.T) {
		// Filter by a kind that doesn't exist in the fixture → 0 hits.
		hits, err := reader.FindDocumentKey(ctx, "navigation", "package_json_deps", "", 10)
		if err != nil {
			t.Fatalf("FindDocumentKey: %v", err)
		}
		if len(hits) != 0 {
			t.Errorf("kind=package_json_deps: got %d hits, want 0", len(hits))
		}
	})

	t.Run("find_document_key_empty_result", func(t *testing.T) {
		hits, err := reader.FindDocumentKey(ctx, "xyzNoSuchKey", "", "", 10)
		if err != nil {
			t.Fatalf("FindDocumentKey: %v", err)
		}
		if hits == nil {
			t.Errorf("expected non-nil empty slice; got nil")
		}
		if len(hits) != 0 {
			t.Errorf("got %d hits, want 0", len(hits))
		}
	})

	// v3.3 B3: package.json deps
	t.Run("package_json_deps_indexed", func(t *testing.T) {
		hits, err := reader.FindDocumentKey(ctx, "react", "package_json_deps", "", 10)
		if err != nil {
			t.Fatalf("FindDocumentKey react: %v", err)
		}
		// react + react-dom
		if len(hits) < 2 {
			t.Errorf("expected >= 2 react entries; got %d (%v)", len(hits), hits)
		}
		// Exact match wins ordering — "react" should be first.
		if hits[0].Key != "react" {
			t.Errorf("first hit key: got %q, want %q", hits[0].Key, "react")
		}
		if hits[0].Value != "^18.0.0" {
			t.Errorf("react value: got %q, want %q", hits[0].Value, "^18.0.0")
		}
		if hits[0].Path != "package.json" {
			t.Errorf("path: got %q, want %q", hits[0].Path, "package.json")
		}
	})

	t.Run("package_json_workspace_value", func(t *testing.T) {
		hits, err := reader.FindDocumentKey(ctx, "@codesphere/utils-common", "package_json_deps", "", 5)
		if err != nil {
			t.Fatalf("FindDocumentKey scoped pkg: %v", err)
		}
		if len(hits) == 0 || hits[0].Value != "workspace:*" {
			t.Errorf("workspace dep: %+v", hits)
		}
	})

	t.Run("package_json_typescript_dev_dep", func(t *testing.T) {
		hits, err := reader.FindDocumentKey(ctx, "typescript", "package_json_deps", "", 5)
		if err != nil {
			t.Fatalf("FindDocumentKey typescript: %v", err)
		}
		if len(hits) == 0 {
			t.Errorf("expected typescript dep entry")
		}
	})

	// v3.3 B3: go.mod requires
	t.Run("go_mod_requires_indexed", func(t *testing.T) {
		hits, err := reader.FindDocumentKey(ctx, "github.com/foo/bar", "go_mod_requires", "", 5)
		if err != nil {
			t.Fatalf("FindDocumentKey: %v", err)
		}
		if len(hits) == 0 {
			t.Fatalf("expected github.com/foo/bar entry; got 0")
		}
		if hits[0].Value != "v1.2.3" {
			t.Errorf("value: got %q, want %q", hits[0].Value, "v1.2.3")
		}
	})

	t.Run("go_mod_indirect_marked", func(t *testing.T) {
		hits, err := reader.FindDocumentKey(ctx, "stretchr/testify", "go_mod_requires", "", 5)
		if err != nil {
			t.Fatalf("FindDocumentKey: %v", err)
		}
		if len(hits) == 0 {
			t.Fatalf("expected testify entry")
		}
		if !strings.Contains(hits[0].Value, "indirect") {
			t.Errorf("expected indirect marker; got %q", hits[0].Value)
		}
	})

	t.Run("stats_documents_by_kind", func(t *testing.T) {
		s, err := reader.Stats(ctx)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if s.DocumentsByKind["i18n_json"] == 0 {
			t.Errorf("expected i18n_json entries in stats")
		}
		if s.DocumentsByKind["package_json_deps"] == 0 {
			t.Errorf("expected package_json_deps entries in stats")
		}
		if s.DocumentsByKind["go_mod_requires"] == 0 {
			t.Errorf("expected go_mod_requires entries in stats")
		}
	})

	t.Run("fts_index_populated", func(t *testing.T) {
		// The MATCH-based FTS query should find entries by partial key
		// — required for the v3.3 B2 ticket (`find_document_key`) but
		// pinned here so the trigger config is right at the schema level.
		var count int
		if err := ix.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM documents_fts WHERE documents_fts MATCH ?`,
			"navigation").Scan(&count); err != nil {
			t.Fatalf("fts match: %v", err)
		}
		if count == 0 {
			t.Errorf("expected FTS hits for 'navigation'; got 0")
		}
	})
}
