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

// SessionCost is the v3.4 A2 byte+token budget for one session.
// Bytes are exact (drawn from telemetry + external JSONL); tokens are
// a directional estimate computed via the configurable chars-per-token
// ratio. The number is **NOT** a billing-accurate token count — it is
// a byte proxy. The point is to surface cost trends across many
// sessions, not to bill anything.
//
// MycoInputBytes / MycoOutputBytes come from the per-call telemetry
// log (Recorder writes them). FallbackInputBytes / FallbackOutputBytes
// come from the v3.4 A1 session-tracking-hook stream. PerToolBytes
// holds (myco + fallback) tools side-by-side ordered by total
// contribution, so a markdown export can render a single ranked table.
type SessionCost struct {
	CharsPerToken float64 `json:"chars_per_token"`

	MycoInputBytes      int64 `json:"myco_input_bytes"`
	MycoOutputBytes     int64 `json:"myco_output_bytes"`
	FallbackInputBytes  int64 `json:"fallback_input_bytes"`
	FallbackOutputBytes int64 `json:"fallback_output_bytes"`

	TotalInputBytes  int64 `json:"total_input_bytes"`
	TotalOutputBytes int64 `json:"total_output_bytes"`
	TotalBytes       int64 `json:"total_bytes"` // in + out, the agent-LLM context cost

	EstimatedTokens int64 `json:"estimated_tokens"` // TotalBytes / CharsPerToken
	MycoTokens      int64 `json:"myco_tokens"`
	FallbackTokens  int64 `json:"fallback_tokens"`

	// v3.4 A3 — counterfactual ("without myco") modelled cost.
	// MycoCounterfactualBytes: per-tool multipliers applied to each
	// myco call's actual OutputBytes, summed.
	// WithoutMycoEstimate: counterfactual + fallback (the fallback
	// side stays as-is — without myco the agent would have done at
	// least these fallbacks, probably more; treating it as a lower
	// bound).
	// EstimatedSavingsBytes: WithoutMycoEstimate - TotalBytes. Negative
	// means myco cost more than the modelled alternative (likely
	// adoption-fixed-point gaps; see G2 from the Go field test).
	// SavingsRatio: savings / without_myco, [-1.0, 1.0]. Positive
	// means myco saved bytes; the headline number for trends.
	MycoCounterfactualBytes  int64   `json:"myco_counterfactual_bytes"`
	MycoCounterfactualTokens int64   `json:"myco_counterfactual_tokens"`
	WithoutMycoEstimateBytes int64   `json:"without_myco_estimate_bytes"`
	WithoutMycoEstimateTokens int64  `json:"without_myco_estimate_tokens"`
	EstimatedSavingsBytes    int64   `json:"estimated_savings_bytes"`
	EstimatedSavingsTokens   int64   `json:"estimated_savings_tokens"`
	SavingsRatio             float64 `json:"savings_ratio"`

	// CounterfactualQualityMix is the count of myco calls per estimate-
	// quality bucket. Lets the renderer say "savings include N
	// low-quality estimates from graph tools" so the user can
	// downweight if the mix is dominated by `low`.
	CounterfactualQualityMix map[EstimateQuality]int `json:"counterfactual_quality_mix,omitempty"`

	PerTool []ToolCost `json:"per_tool"`
}

// ToolCost is one row of the per-tool cost breakdown that ships inside
// SessionCost.PerTool. Source is "myco" or "fallback" so renderers can
// group or color-code the two halves.
//
// CounterfactualBytes is populated only for myco rows: the modelled
// byte cost of the equivalent fallback (grep/Read/find) operation.
// Renderers can subtract OutputBytes to show per-tool savings.
// EstimateQuality grades how much trust to put in that figure.
type ToolCost struct {
	Tool                string          `json:"tool"`
	Source              string          `json:"source"` // "myco" | "fallback"
	Count               int             `json:"count"`
	InputBytes          int64           `json:"input_bytes"`
	OutputBytes         int64           `json:"output_bytes"`
	TotalBytes          int64           `json:"total_bytes"`
	EstimatedTokens     int64           `json:"estimated_tokens"`
	CounterfactualBytes int64           `json:"counterfactual_bytes,omitempty"`
	EstimateQuality     EstimateQuality `json:"estimate_quality,omitempty"`
}

// ComputeSessionCost rolls the myco summaries (excluding the "all"
// rollup, since it would double-count) and the fallback summaries into
// a single SessionCost. charsPerToken <= 0 falls back to 4.0 — every
// caller goes through this so the conversion ratio stays consistent
// across the markdown export, the JSON export, and any future
// dashboards.
func ComputeSessionCost(myco []Summary, fallback []ExternalSummary, charsPerToken float64) SessionCost {
	if charsPerToken <= 0 {
		charsPerToken = 4.0
	}
	cost := SessionCost{
		CharsPerToken:            charsPerToken,
		CounterfactualQualityMix: map[EstimateQuality]int{},
	}

	for _, s := range myco {
		if s.Tool == "all" {
			continue // synthetic rollup; would double-count if we summed it
		}
		cost.MycoInputBytes += s.InputBytes
		cost.MycoOutputBytes += s.OutputBytes
		total := s.InputBytes + s.OutputBytes

		// v3.4 A3: model the without-myco cost per tool. The multiplier
		// applies to the actual output bytes (what the agent's context
		// absorbed). Quality is rolled up so the renderer can warn
		// when the savings number is dominated by low-quality estimates.
		est := EstimateCounterfactual(s.Tool, s.OutputBytes)
		if est.Quality != EstimateQualityNone {
			cost.MycoCounterfactualBytes += est.Bytes
			cost.CounterfactualQualityMix[est.Quality] += s.Count
		}

		cost.PerTool = append(cost.PerTool, ToolCost{
			Tool:                s.Tool,
			Source:              "myco",
			Count:               s.Count,
			InputBytes:          s.InputBytes,
			OutputBytes:         s.OutputBytes,
			TotalBytes:          total,
			EstimatedTokens:     bytesToTokens(total, charsPerToken),
			CounterfactualBytes: est.Bytes,
			EstimateQuality:     est.Quality,
		})
	}
	for _, s := range fallback {
		in := int64(s.InputBytes)
		out := int64(s.OutputBytes)
		cost.FallbackInputBytes += in
		cost.FallbackOutputBytes += out
		total := in + out
		cost.PerTool = append(cost.PerTool, ToolCost{
			Tool:            s.Tool,
			Source:          "fallback",
			Count:           s.Count,
			InputBytes:      in,
			OutputBytes:     out,
			TotalBytes:      total,
			EstimatedTokens: bytesToTokens(total, charsPerToken),
		})
	}

	cost.TotalInputBytes = cost.MycoInputBytes + cost.FallbackInputBytes
	cost.TotalOutputBytes = cost.MycoOutputBytes + cost.FallbackOutputBytes
	cost.TotalBytes = cost.TotalInputBytes + cost.TotalOutputBytes
	cost.MycoTokens = bytesToTokens(cost.MycoInputBytes+cost.MycoOutputBytes, charsPerToken)
	cost.FallbackTokens = bytesToTokens(cost.FallbackInputBytes+cost.FallbackOutputBytes, charsPerToken)
	cost.EstimatedTokens = cost.MycoTokens + cost.FallbackTokens

	// v3.4 A3: derive the without-myco estimate. The fallback half stays
	// as-is — without myco the agent would have done at least these
	// fallbacks (probably more), so treating it as a lower bound is the
	// honest move. Counterfactual + actual fallback gives the modelled
	// "no-myco" budget; the delta vs. the real total is the savings.
	cost.MycoCounterfactualTokens = bytesToTokens(cost.MycoCounterfactualBytes, charsPerToken)
	cost.WithoutMycoEstimateBytes = cost.MycoCounterfactualBytes + cost.FallbackInputBytes + cost.FallbackOutputBytes
	cost.WithoutMycoEstimateTokens = bytesToTokens(cost.WithoutMycoEstimateBytes, charsPerToken)
	cost.EstimatedSavingsBytes = cost.WithoutMycoEstimateBytes - cost.TotalBytes
	cost.EstimatedSavingsTokens = cost.WithoutMycoEstimateTokens - cost.EstimatedTokens
	if cost.WithoutMycoEstimateBytes > 0 {
		cost.SavingsRatio = float64(cost.EstimatedSavingsBytes) / float64(cost.WithoutMycoEstimateBytes)
	}
	if len(cost.CounterfactualQualityMix) == 0 {
		cost.CounterfactualQualityMix = nil
	}

	// Rank per-tool rows by total bytes desc, then stable by tool name
	// so the markdown table reads top-of-cost downward.
	sort.SliceStable(cost.PerTool, func(i, j int) bool {
		if cost.PerTool[i].TotalBytes != cost.PerTool[j].TotalBytes {
			return cost.PerTool[i].TotalBytes > cost.PerTool[j].TotalBytes
		}
		return cost.PerTool[i].Tool < cost.PerTool[j].Tool
	})
	return cost
}

// bytesToTokens converts raw bytes to an estimated token count via the
// configured chars-per-token ratio. Rounded to the nearest int64; zero
// when bytes are zero. Lives here so every consumer of SessionCost
// gets the same conversion without duplicating the rounding rule.
func bytesToTokens(bytes int64, charsPerToken float64) int64 {
	if bytes == 0 || charsPerToken <= 0 {
		return 0
	}
	return int64(float64(bytes)/charsPerToken + 0.5)
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
