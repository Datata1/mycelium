// Integration tests for the doctor freshness surface: the sampled
// index_freshness check, the exact DeepFreshness walk diff, and the
// SampleFiles/AllFilePaths reader methods they build on.
package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datata1/mycelium/internal/doctor"
	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/parser/python"
	"github.com/datata1/mycelium/internal/parser/typescript"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
)

func indexSampleFixture(t *testing.T) (string, *pipeline.Pipeline, *query.Reader) {
	t.Helper()
	dst := copyFixture(t, "testdata/fixtures/sample")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	ix := openIndex(t, filepath.Join(dst, ".mycelium", "index.db"))
	t.Cleanup(func() { _ = ix.Close() })

	reg := parser.NewRegistry()
	reg.Register(golang.New())
	reg.Register(typescript.New())
	reg.Register(python.New())
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: reg,
		Walker:   repo.NewWalker(dst, []string{"**/*.go", "src/**/*.ts", "py/**/*.py"}, nil, 0),
	}
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("index: %v", err)
	}
	return dst, p, query.NewReader(ix.DB())
}

func findCheck(rep doctor.Report, name string) *doctor.Check {
	for i := range rep.Checks {
		if rep.Checks[i].Name == name {
			return &rep.Checks[i]
		}
	}
	return nil
}

func TestIntegration_DoctorFreshness(t *testing.T) {
	t.Parallel()
	dst, _, reader := indexSampleFixture(t)
	ctx := context.Background()
	th := doctor.DefaultThresholds()

	t.Run("fresh_index_passes", func(t *testing.T) {
		rep, err := doctor.Run(ctx, reader, th, dst, "")
		if err != nil {
			t.Fatalf("doctor: %v", err)
		}
		c := findCheck(rep, "index_freshness")
		if c == nil {
			t.Fatal("index_freshness check missing")
		}
		if c.Level != doctor.LevelPass {
			t.Errorf("level = %s (%s), want pass", c.Level, c.Message)
		}
	})

	// Sample fixture has 4 files; deleting one and touching another is
	// 50% of the sample — well past FreshnessFailRatio.
	if err := os.Remove(filepath.Join(dst, "py", "worker.py")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dst, "main.go"), future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	t.Run("stale_index_flagged", func(t *testing.T) {
		rep, err := doctor.Run(ctx, reader, th, dst, "")
		if err != nil {
			t.Fatalf("doctor: %v", err)
		}
		c := findCheck(rep, "index_freshness")
		if c == nil {
			t.Fatal("index_freshness check missing")
		}
		if c.Level == doctor.LevelPass {
			t.Errorf("level = pass, want warn/fail; message: %s", c.Message)
		}
		if c.Detail["missing_on_disk"].(int) != 1 {
			t.Errorf("missing_on_disk = %v, want 1", c.Detail["missing_on_disk"])
		}
		if c.Detail["mtime_newer"].(int) != 1 {
			t.Errorf("mtime_newer = %v, want 1", c.Detail["mtime_newer"])
		}
	})

	t.Run("sample_files_reader", func(t *testing.T) {
		rows, err := reader.SampleFiles(ctx, 2)
		if err != nil {
			t.Fatalf("SampleFiles: %v", err)
		}
		if len(rows) != 2 {
			t.Errorf("sampled %d rows, want 2", len(rows))
		}
		for _, r := range rows {
			if r.Path == "" || r.MTimeNS == 0 {
				t.Errorf("incomplete row: %+v", r)
			}
		}
	})
}

func TestIntegration_DoctorDeepFreshness(t *testing.T) {
	t.Parallel()
	dst, p, reader := indexSampleFixture(t)
	ctx := context.Background()

	// Ghost row: delete a file without reconciling. New unindexed file:
	// create one the index has never seen.
	if err := os.Remove(filepath.Join(dst, "src", "auth.ts")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "fresh.go"), []byte("package fresh\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	walked, err := p.WalkedPaths()
	if err != nil {
		t.Fatalf("WalkedPaths: %v", err)
	}
	rows, err := reader.AllFilePaths(ctx)
	if err != nil {
		t.Fatalf("AllFilePaths: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("indexed rows = %d, want 4", len(rows))
	}
	indexed := make([]string, len(rows))
	for i, r := range rows {
		indexed[i] = r.Path
	}

	c := doctor.DeepFreshness(walked, indexed, 5)
	if c.Level != doctor.LevelWarn {
		t.Errorf("level = %s (%s), want warn", c.Level, c.Message)
	}
	if c.Detail["on_disk_not_indexed_count"].(int) != 1 {
		t.Errorf("on_disk_not_indexed = %v, want 1 (fresh.go)", c.Detail["on_disk_not_indexed"])
	}
	if c.Detail["indexed_not_walked_count"].(int) != 1 {
		t.Errorf("indexed_not_walked = %v, want 1 (auth.ts)", c.Detail["indexed_not_walked"])
	}
}
