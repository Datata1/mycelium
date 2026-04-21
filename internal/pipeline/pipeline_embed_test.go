package pipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/index"
	"github.com/jdwiederstein/mycelium/internal/parser"
	"github.com/jdwiederstein/mycelium/internal/parser/golang"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/repo"
)

// TestEmbedRoundTrip writes a tiny Go fixture, indexes it with a Fake
// embedder, runs the embed worker for a short burst, and verifies that
// semantic search returns the expected top hit.
func TestEmbedRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), `package main

// LoadConfig reads settings from a YAML file.
func LoadConfig(path string) error {
	return nil
}

// SaveConfig writes settings to a YAML file.
func SaveConfig(path string) error {
	return nil
}

// AuthenticateUser validates a login attempt against the user store.
func AuthenticateUser(email, password string) error {
	return nil
}
`)

	ix, err := index.Open(filepath.Join(dir, ".mycelium", "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(golang.New())
	walker := repo.NewWalker(dir, []string{"**/*.go"}, nil, 0)
	fake := embed.NewFake(64)

	p := &pipeline.Pipeline{Index: ix, Registry: reg, Walker: walker, Embedder: fake}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("run pipeline: %v", err)
	}

	// Drain the embed queue synchronously.
	worker := &pipeline.EmbedWorker{
		Index:     ix,
		Embedder:  fake,
		BatchSize: 16,
		Logger:    pipeline.NullLogger{},
	}
	// Run until the queue is empty rather than for a fixed duration, so the
	// test doesn't rely on timing.
	for i := 0; i < 10; i++ {
		jobs, _ := ix.FetchPending(ctx, 16)
		if len(jobs) == 0 {
			break
		}
		// Call processBatch via a short-lived Run — easier than exporting the
		// method. We cap the outer loop because the worker polls on an
		// interval.
		done := make(chan struct{})
		runCtx, runCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		go func() {
			worker.Run(runCtx)
			close(done)
		}()
		<-done
		runCancel()
	}

	reader := query.NewReader(ix.DB())
	status, err := reader.EmbeddingStatus(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.ChunksEmbed == 0 {
		t.Fatalf("no chunks were embedded; status=%+v", status)
	}
	if status.Pending != 0 {
		t.Fatalf("queue not fully drained; pending=%d", status.Pending)
	}

	searcher := &query.Searcher{Reader: reader, Embedder: fake}
	hits, err := searcher.SearchSemantic(ctx, "validate a login attempt", 3, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("got zero hits")
	}
	if hits[0].Qualified != "main.AuthenticateUser" {
		t.Errorf("expected top hit AuthenticateUser, got %s (score=%.3f)", hits[0].Qualified, hits[0].Score)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
