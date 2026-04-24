//go:build !linux

package doctor

// inotifyCheck is Linux-only. Other platforms return nil so the check
// simply doesn't appear in the report.
func inotifyCheck(string, Thresholds) *Check { return nil }
