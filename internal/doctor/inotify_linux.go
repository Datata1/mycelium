//go:build linux

package doctor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// inotifyCheck walks the repo once, counts directories we'd register
// with fsnotify (skipping .git / .mycelium to match the live watcher),
// and reports the ratio against /proc/sys/fs/inotify/max_user_watches.
//
// Returns nil when the limit file is unreadable — a missing /proc is
// not an actionable signal, and we don't want to pollute the report.
func inotifyCheck(root string, th Thresholds) *Check {
	maxWatches, err := readInotifyMax()
	if err != nil {
		return nil
	}
	dirs, err := countDirs(root)
	if err != nil {
		return nil
	}
	var ratio float64
	if maxWatches > 0 {
		ratio = float64(dirs) / float64(maxWatches)
	}
	lvl := LevelPass
	switch {
	case ratio >= th.InotifyFail:
		lvl = LevelFail
	case ratio >= th.InotifyWarn:
		lvl = LevelWarn
	}
	msg := fmt.Sprintf(
		"%d repo dirs vs max_user_watches=%d (%.1f%%); switch to watchman or raise the limit when this climbs",
		dirs, maxWatches, ratio*100,
	)
	if lvl != LevelPass {
		msg += " — set watcher.backend: watchman in .mycelium.yml or `sudo sysctl fs.inotify.max_user_watches=524288`"
	}
	return &Check{
		Name:    "inotify_headroom",
		Level:   lvl,
		Message: msg,
		Detail: map[string]any{
			"repo_dirs":        dirs,
			"max_user_watches": maxWatches,
			"ratio":            ratio,
		},
	}
}

func readInotifyMax() (int, error) {
	b, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return n, nil
}

// countDirs walks root and counts directories that fsnotify would
// register. .git and .mycelium are skipped in both trees (same rule
// the watcher enforces) so the numbers line up.
func countDirs(root string) (int, error) {
	var n int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if (name == ".git" || name == ".mycelium") && path != root {
			return filepath.SkipDir
		}
		n++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
