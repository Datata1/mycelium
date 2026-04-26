package telemetry

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestFileRecorder_AppendAndAggregate exercises the round-trip: open a
// fresh log, write several records, aggregate. Per-tool counts and the
// "all" rollup must agree with what we wrote, and concurrent writes
// from many goroutines must not corrupt the file.
func TestFileRecorder_AppendAndAggregate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")

	rec, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const callsPerTool = 20
	tools := []string{"find_symbol", "get_neighborhood", "stats"}
	var wg sync.WaitGroup
	for _, tool := range tools {
		for i := 0; i < callsPerTool; i++ {
			wg.Add(1)
			go func(tool string, i int) {
				defer wg.Done()
				if err := rec.Record(Record{
					Tool:        tool,
					InputBytes:  10 * (i + 1),
					OutputBytes: 100 * (i + 1),
					DurationMS:  int64(i + 1),
					OK:          i%5 != 0,
				}); err != nil {
					t.Errorf("record: %v", err)
				}
			}(tool, i)
		}
	}
	wg.Wait()
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	summaries, err := Aggregate(path)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}

	wantTotal := callsPerTool * len(tools)
	byTool := map[string]Summary{}
	for _, s := range summaries {
		byTool[s.Tool] = s
	}
	for _, tool := range tools {
		s, ok := byTool[tool]
		if !ok {
			t.Fatalf("missing summary for %q; got %v", tool, byTool)
		}
		if s.Count != callsPerTool {
			t.Errorf("tool %s: count = %d, want %d", tool, s.Count, callsPerTool)
		}
		if s.OK != callsPerTool*4/5 {
			t.Errorf("tool %s: ok = %d, want %d", tool, s.OK, callsPerTool*4/5)
		}
	}
	all, ok := byTool["all"]
	if !ok {
		t.Fatal("missing 'all' rollup")
	}
	if all.Count != wantTotal {
		t.Errorf("all.Count = %d, want %d", all.Count, wantTotal)
	}
	if all.MeanDuration <= 0 {
		t.Error("expected positive mean duration")
	}
	if all.P95Duration < all.P50Duration {
		t.Errorf("p95 (%v) < p50 (%v) — sort bug", all.P95Duration, all.P50Duration)
	}
}

// TestDisabled is a sanity check that the no-op recorder exists and
// satisfies the interface — important because the daemon will type-
// assert on Recorder, not the concrete type.
func TestDisabled(t *testing.T) {
	t.Parallel()
	var r Recorder = Disabled{}
	if err := r.Record(Record{Tool: "x", Timestamp: time.Now()}); err != nil {
		t.Errorf("Disabled.Record returned error: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Disabled.Close returned error: %v", err)
	}
}

// TestAggregate_MissingFile codifies the friendly behavior: telemetry
// configured but no calls yet (=> file doesn't exist) returns
// (nil, nil) so the CLI can render a hint instead of an error.
func TestAggregate_MissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nope.jsonl")
	summaries, err := Aggregate(path)
	if err != nil {
		t.Fatalf("Aggregate on missing file: %v", err)
	}
	if summaries != nil {
		t.Errorf("expected nil summaries for missing file; got %v", summaries)
	}
}
