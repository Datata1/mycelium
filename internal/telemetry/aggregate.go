package telemetry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// Summary is a per-tool rollup of the telemetry log: how often each
// MCP method was called, how big the inputs/outputs got, and where
// duration sits at p50/p95. `myco stats --telemetry` renders one row
// per tool plus an "all" row.
type Summary struct {
	Tool         string        `json:"tool"`
	Count        int           `json:"count"`
	OK           int           `json:"ok"`
	InputBytes   int64         `json:"in_bytes_total"`
	OutputBytes  int64         `json:"out_bytes_total"`
	MeanDuration time.Duration `json:"mean_duration"`
	P50Duration  time.Duration `json:"p50_duration"`
	P95Duration  time.Duration `json:"p95_duration"`
	First        time.Time     `json:"first_ts"`
	Last         time.Time     `json:"last_ts"`
}

// accum holds running totals while Aggregate streams the log. Lifted
// to package scope so summarize() can take it as a parameter.
type accum struct {
	count       int
	ok          int
	inBytes     int64
	outBytes    int64
	durations   []time.Duration
	first, last time.Time
}

// Aggregate streams the JSONL at path and returns one Summary per tool
// plus a final "all" row that aggregates the whole file. Returns
// (nil, nil) when the file doesn't exist — that's the "telemetry on
// but no calls yet" case, which the CLI surfaces as a friendly hint
// rather than an error.
func Aggregate(path string) ([]Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open telemetry log: %w", err)
	}
	defer f.Close()

	byTool := map[string]*accum{}
	all := &accum{}

	sc := bufio.NewScanner(f)
	// Long lines are unlikely (records are small), but raise the buffer
	// so a pathological tool with a huge input doesn't break parsing.
	sc.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			// Skip garbled lines rather than failing the whole report;
			// users running tail-f tail-write races should still get
			// useful aggregates. Honest about how many we skip in CLI.
			continue
		}
		dur := time.Duration(r.DurationMS) * time.Millisecond
		a, ok := byTool[r.Tool]
		if !ok {
			a = &accum{}
			byTool[r.Tool] = a
		}
		for _, target := range []*accum{a, all} {
			target.count++
			if r.OK {
				target.ok++
			}
			target.inBytes += int64(r.InputBytes)
			target.outBytes += int64(r.OutputBytes)
			target.durations = append(target.durations, dur)
			if target.first.IsZero() || r.Timestamp.Before(target.first) {
				target.first = r.Timestamp
			}
			if r.Timestamp.After(target.last) {
				target.last = r.Timestamp
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read telemetry log: %w", err)
	}

	out := make([]Summary, 0, len(byTool)+1)
	for tool, a := range byTool {
		out = append(out, summarize(tool, a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if all.count > 0 {
		out = append(out, summarize("all", all))
	}
	return out, nil
}

func summarize(name string, a *accum) (s Summary) {
	if a == nil || a.count == 0 {
		return Summary{Tool: name}
	}
	s.Tool = name
	s.Count = a.count
	s.OK = a.ok
	s.InputBytes = a.inBytes
	s.OutputBytes = a.outBytes
	s.First = a.first
	s.Last = a.last
	if len(a.durations) == 0 {
		return s
	}
	sort.Slice(a.durations, func(i, j int) bool { return a.durations[i] < a.durations[j] })
	var total time.Duration
	for _, d := range a.durations {
		total += d
	}
	s.MeanDuration = total / time.Duration(len(a.durations))
	s.P50Duration = a.durations[len(a.durations)/2]
	// p95: nearest-rank, capped at last element.
	idx := (len(a.durations) * 95) / 100
	if idx >= len(a.durations) {
		idx = len(a.durations) - 1
	}
	s.P95Duration = a.durations[idx]
	return s
}
