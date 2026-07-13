package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datata1/mycelium/internal/ipc"
)

func gateReport(checks ...ipc.VerifyCheck) ipc.VerifyReport {
	return ipc.VerifyReport{Since: "HEAD", Checks: checks}
}

func TestVerifyGateOutput(t *testing.T) {
	t.Run("removal_fail_blocks_with_call_sites", func(t *testing.T) {
		rep := gateReport(ipc.VerifyCheck{Name: "removed_but_referenced", Level: "fail"})
		rep.Removed = []ipc.RemovedSymbol{
			{
				Qualified: "demo.Widget",
				Danglers: []ipc.VerifyDangler{
					{Path: "b.go", Line: 4, Exact: true},
					{Path: "c.go", Line: 9, Exact: false}, // short-name evidence stays out of the reason
				},
			},
		}
		out := verifyGateOutput(rep)
		if out == "" {
			t.Fatal("expected block JSON")
		}
		var payload struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("invalid JSON: %v (%s)", err, out)
		}
		if payload.Decision != "block" {
			t.Errorf("decision = %q, want block", payload.Decision)
		}
		if !strings.Contains(payload.Reason, "b.go:4") || !strings.Contains(payload.Reason, "demo.Widget") {
			t.Errorf("reason missing call site: %q", payload.Reason)
		}
		if strings.Contains(payload.Reason, "c.go:9") {
			t.Errorf("short-name dangler must not appear in reason: %q", payload.Reason)
		}
	})

	t.Run("stale_index_fail_does_not_block", func(t *testing.T) {
		rep := gateReport(
			ipc.VerifyCheck{Name: "index_fresh_for_changes", Level: "fail"},
			ipc.VerifyCheck{Name: "removed_but_referenced", Level: "pass"},
		)
		if out := verifyGateOutput(rep); out != "" {
			t.Errorf("stale index must not block; got %q", out)
		}
	})

	t.Run("warn_does_not_block", func(t *testing.T) {
		rep := gateReport(ipc.VerifyCheck{Name: "removed_but_referenced", Level: "warn"})
		if out := verifyGateOutput(rep); out != "" {
			t.Errorf("warn must not block; got %q", out)
		}
	})

	t.Run("clean_report_does_not_block", func(t *testing.T) {
		rep := gateReport(ipc.VerifyCheck{Name: "git_scope", Level: "pass"})
		if out := verifyGateOutput(rep); out != "" {
			t.Errorf("clean report must not block; got %q", out)
		}
	})
}

// Double installation with and without the gate must stay idempotent
// and only add the verify entry when asked.
func TestInstallSessionHooks_VerifyGate(t *testing.T) {
	root := t.TempDir()

	countStopHooks := func() (verify, annotate int) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, ".claude", "settings.local.json"))
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}
		var raw struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatalf("parse settings: %v", err)
		}
		for _, entry := range raw.Hooks["Stop"] {
			for _, h := range entry.Hooks {
				if strings.Contains(h.Command, "session verify") {
					verify++
				}
				if strings.Contains(h.Command, "session annotate") {
					annotate++
				}
			}
		}
		return verify, annotate
	}

	if err := installSessionHooks(root, "myco", false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if v, a := countStopHooks(); v != 0 || a != 1 {
		t.Fatalf("plain install: verify=%d annotate=%d, want 0/1", v, a)
	}

	if err := installSessionHooks(root, "myco", true); err != nil {
		t.Fatalf("install --verify-gate: %v", err)
	}
	if v, a := countStopHooks(); v != 1 || a != 1 {
		t.Fatalf("gate install: verify=%d annotate=%d, want 1/1", v, a)
	}

	// Idempotency: repeating either form adds nothing.
	if err := installSessionHooks(root, "myco", true); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	if err := installSessionHooks(root, "myco", false); err != nil {
		t.Fatalf("re-install plain: %v", err)
	}
	if v, a := countStopHooks(); v != 1 || a != 1 {
		t.Fatalf("after re-installs: verify=%d annotate=%d, want 1/1", v, a)
	}
}
