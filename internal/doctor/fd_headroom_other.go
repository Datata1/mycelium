//go:build !linux

package doctor

// daemonFDHeadroomCheck is Linux-only: macOS / Windows lack
// `/proc/<pid>/fd` and counting the daemon's fds requires either
// `lsof` (sub-process, brittle) or platform-specific syscall paths.
// On macOS the v4 T2 layer 3 RaiseFileDescriptorLimit() startup bump
// already preempts the most common EMFILE case (256 → 10240 fd
// soft cap), so the doctor signal is less load-bearing there.
// Returns nil so the doctor renderer simply omits the check.
func daemonFDHeadroomCheck(_ string, _ Thresholds) *Check {
	return nil
}
