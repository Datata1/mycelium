//go:build linux

package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestDaemonFDHeadroomCheck_SkipsWhenNoPIDFile is the v4 T2 layer 1
// "no daemon" path: missing pid file → nil result, doctor renders
// nothing for the check. Avoids a misleading "0 open fds" report
// when no daemon was ever running.
func TestDaemonFDHeadroomCheck_SkipsWhenNoPIDFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".mycelium")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No daemon.pid file written.
	got := daemonFDHeadroomCheck(stateDir, DefaultThresholds())
	if got != nil {
		t.Errorf("expected nil when pid file is absent; got %+v", got)
	}
}

// TestDaemonFDHeadroomCheck_SkipsOnStalePID covers the v4 T2 case
// where the daemon died without cleaning up its pid file. The check
// must skip rather than report 0 open fds (which would WARN under
// the FDHeadroom threshold of 0 / soft = 0%).
func TestDaemonFDHeadroomCheck_SkipsOnStalePID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".mycelium")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// PID 1 is init — exists on every Linux box but isn't us.
	// Use a deliberately-impossible PID instead so /proc/<pid>
	// definitely doesn't exist. 0x7fffffff is the int32 max,
	// well above any sane process count.
	if err := os.WriteFile(
		filepath.Join(stateDir, "daemon.pid"),
		[]byte("2147483647\n"), 0o644,
	); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	got := daemonFDHeadroomCheck(stateDir, DefaultThresholds())
	if got != nil {
		t.Errorf("expected nil for stale PID; got %+v", got)
	}
}

// TestDaemonFDHeadroomCheck_RealProcess verifies the full path against
// the test process itself: write our own PID to the pid file, run
// the check, expect a populated Check with sane numbers.
func TestDaemonFDHeadroomCheck_RealProcess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".mycelium")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pid := os.Getpid()
	if err := os.WriteFile(
		filepath.Join(stateDir, "daemon.pid"),
		[]byte(fmt.Sprintf("%d\n", pid)), 0o644,
	); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	got := daemonFDHeadroomCheck(stateDir, DefaultThresholds())
	if got == nil {
		t.Fatal("expected non-nil check; got nil")
	}
	if got.Name != "daemon_fd_headroom" {
		t.Errorf("Name = %q, want daemon_fd_headroom", got.Name)
	}
	openFDs, _ := got.Detail["open_fds"].(int)
	if openFDs <= 0 {
		t.Errorf("open_fds = %d, want > 0 (this test process owns at least stdin/out/err)", openFDs)
	}
	soft, _ := got.Detail["soft_limit"].(uint64)
	if soft <= 0 {
		t.Errorf("soft_limit = %d, want > 0", soft)
	}
	if openFDs >= int(soft) {
		t.Errorf("open_fds (%d) >= soft_limit (%d) — test process should not be near the limit",
			openFDs, soft)
	}
}
