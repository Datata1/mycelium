package bench

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jdwiederstein/mycelium/internal/ipc"
	"github.com/jdwiederstein/mycelium/internal/telemetry"
)

// Run executes every Case in the corpus against the daemon at client,
// runs the equivalent shell fallback against repoRoot, and returns
// per-case Rows ready for rendering. The driftThreshold determines
// which Rows have OK = false; the renderer uses that to render
// "DRIFT" status and to decide on the bench's exit code.
//
// `language` is the dominant repo language (Stats.ByLang's largest
// entry, or "" for unknown). It selects the per-language multiplier
// override when present, falling back to the default. v4 B3 wires
// the plumbing; F1/F2 field tests will populate the per-language
// override entries.
//
// Run is pure — no globals, no filesystem mutation outside reading
// the fallback files. The daemon roundtrips and shell-out timing
// are inherently impure but bounded to the Cases.
func Run(client *ipc.Client, repoRoot string, corpus Corpus, language string, driftThreshold float64) []Row {
	rows := make([]Row, 0, len(corpus.Cases))
	for _, bc := range corpus.Cases {
		rows = append(rows, runOne(client, repoRoot, bc, language, driftThreshold))
	}
	return rows
}

func runOne(client *ipc.Client, repoRoot string, bc Case, language string, driftThreshold float64) Row {
	row := Row{Tool: bc.Tool, Note: bc.Note}

	// Myco side: capture raw response bytes — matches what the daemon
	// records as out_bytes in telemetry.jsonl.
	var raw json.RawMessage
	mStart := time.Now()
	if err := client.Call(bc.Method, bc.Params, &raw); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "unknown method") {
			// The daemon is older than the binary running this bench.
			// Distinguish from a real RPC failure so the user knows
			// to restart rather than chase a calibration regression.
			msg += "  (restart the daemon — its build predates this method)"
		}
		row.Err = "myco: " + msg
		return row
	}
	row.MycoMS = time.Since(mStart).Milliseconds()
	row.MycoBytes = int64(len(raw))

	// Fallback side: stdout byte count (grep/find) or file size (wc -c).
	fStart := time.Now()
	switch {
	case bc.FallbackFile != "":
		info, err := os.Stat(filepath.Join(repoRoot, bc.FallbackFile))
		if err != nil {
			row.Err = "fallback: " + err.Error()
			return row
		}
		row.FallbackBytes = info.Size()
	case bc.FallbackCmd != "":
		c := exec.Command("bash", "-c", bc.FallbackCmd)
		c.Dir = repoRoot
		out, _ := c.Output() // grep returns 1 on no match — we still want the bytes
		row.FallbackBytes = int64(len(out))
	}
	row.FallbackMS = time.Since(fStart).Milliseconds()

	// Compare against the model. v4 B3: use the per-language variant
	// so populated overrides take effect; empty language falls through
	// to the global default multiplier (backward-compat with v3.4 A3).
	mul, qual, _ := telemetry.CounterfactualMultiplierFor(bc.Tool, language)
	row.ModelRatio = mul
	row.Quality = string(qual)
	if row.MycoBytes > 0 {
		row.MeasuredRatio = float64(row.FallbackBytes) / float64(row.MycoBytes)
	}
	// Drift uses max(model, 0.01) as the denominator so the zero-
	// multiplier tools (stats/ping, never benched) don't divide by 0
	// if someone adds them to the corpus by accident.
	row.Drift = math.Abs(row.MeasuredRatio-mul) / math.Max(mul, 0.01)
	// Low-quality multipliers self-document as rough — the model
	// already says "don't trust this much". Treat their drift as
	// informational so a single corpus point can't break CI on a
	// graph-walk tool the model never claimed precision for.
	row.OK = row.Drift <= driftThreshold || qual == telemetry.EstimateQualityLow
	return row
}

// PrintTable writes a human-readable table to stdout. Lifted from
// cmd/myco/main.go's printBenchTable so the bench package owns its
// own rendering and the CLI shrinks to a thin orchestrator. The
// renderer prints status `info` for low-quality drift and `DRIFT`
// for everything else over threshold.
func PrintTable(rows []Row, threshold float64, corpusName, language string) {
	hdr := fmt.Sprintf("── counterfactual calibration  (corpus=%s", corpusName)
	if language != "" {
		hdr += "  language=" + language
	}
	hdr += fmt.Sprintf("  drift threshold %.0f%%) ─────────", threshold*100)
	fmt.Println(hdr)
	fmt.Printf("%-20s  %10s  %10s  %8s  %8s  %7s  %8s  %s\n",
		"tool", "myco", "fallback", "measured", "model", "drift", "quality", "status")
	for _, r := range rows {
		status := "ok"
		switch {
		case r.Err != "":
			status = "ERR"
		case !r.OK:
			status = "DRIFT"
		case r.Quality == string(telemetry.EstimateQualityLow) && r.Drift > threshold:
			status = "info" // low-quality drift is expected, not a failure
		}
		fmt.Printf("%-20s  %10s  %10s  %8.2f  %8.2f  %6.0f%%  %8s  %s\n",
			r.Tool,
			humanBytes(r.MycoBytes), humanBytes(r.FallbackBytes),
			r.MeasuredRatio, r.ModelRatio, r.Drift*100,
			r.Quality, status)
		if r.Err != "" {
			fmt.Printf("    error: %s\n", r.Err)
		}
	}
	fmt.Println()
	fmt.Println("measured = fallback_bytes / myco_bytes  (target: should match model)")
	fmt.Println("drift    = |measured - model| / max(model, 0.01)")
	fmt.Println("low-quality estimates (graph tools) can drift more than the threshold")
	fmt.Println("without invalidating the model — interpret with the 'quality' column.")
}

// humanBytes mirrors the cmd/myco/main.go helper of the same name.
// Local copy because the bench package shouldn't depend on cmd/.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffix := "KMGTPE"[exp]
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), suffix)
}
