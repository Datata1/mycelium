//go:build linux

package doctor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// daemonFDHeadroomCheck reads the daemon's PID file and probes
// `/proc/<pid>/fd` to surface "you're approaching EMFILE" warnings
// before the daemon hits the v4 T2 fd-leak failure mode. Returns nil
// when the daemon isn't running (no PID file, or stale PID), when
// /proc isn't readable (sandboxed environments), or when limits
// can't be parsed — none of those are actionable signals worth
// polluting the report with.
//
// Pairs with v4 T2 layer 3 (RaiseFileDescriptorLimit at daemon
// startup): the bump preempts most EMFILE cases on macOS; this
// check surfaces remaining headroom pressure on Linux where
// monorepo-scale fsnotify-watched repos can still exhaust fds.
func daemonFDHeadroomCheck(repoRoot string, th Thresholds) *Check {
	pid, ok := readDaemonPID(repoRoot)
	if !ok {
		return nil
	}
	openFDs, ok := countOpenFDs(pid)
	if !ok {
		return nil
	}
	softLimit, ok := readSoftFDLimit(pid)
	if !ok || softLimit == 0 {
		return nil
	}
	ratio := float64(openFDs) / float64(softLimit)
	lvl := LevelPass
	switch {
	case ratio >= th.FDHeadroomFail:
		lvl = LevelFail
	case ratio >= th.FDHeadroomWarn:
		lvl = LevelWarn
	}
	msg := fmt.Sprintf(
		"daemon pid %d: %d/%d open fds (%.0f%%)",
		pid, openFDs, softLimit, ratio*100,
	)
	if lvl != LevelPass {
		msg += " — set watcher.backend: watchman in .mycelium.yml or raise the system limit"
	}
	return &Check{
		Name:    "daemon_fd_headroom",
		Level:   lvl,
		Message: msg,
		Detail: map[string]any{
			"pid":        pid,
			"open_fds":   openFDs,
			"soft_limit": softLimit,
			"ratio":      ratio,
		},
	}
}

func readDaemonPID(repoRoot string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(repoRoot, ".mycelium", "daemon.pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	// Stale PID detection: if /proc/<pid> doesn't exist, the daemon
	// has died without cleaning up its pid file. Skip rather than
	// emitting a misleading "0 open fds" warning.
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
		return 0, false
	}
	return pid, true
}

func countOpenFDs(pid int) (int, bool) {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

// readSoftFDLimit parses /proc/<pid>/limits for the "Max open files"
// line. Format: a fixed-width table; soft limit is in column 2,
// hard in column 3. Returns the soft limit + true on success.
//
// Example matched line:
//
//	Max open files            1024                 524288               files
func readSoftFDLimit(pid int) (uint64, bool) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/limits", pid))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "Max open files") {
			continue
		}
		// Strip the prefix, then split on whitespace; the soft limit
		// is the first remaining field.
		rest := strings.TrimSpace(strings.TrimPrefix(line, "Max open files"))
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
