//go:build windows

package daemon

// RaiseFileDescriptorLimit is a no-op on Windows. The RLIMIT_NOFILE
// abstraction doesn't apply — Windows's per-process handle limit is
// extremely high (16M+) and out of reach of any sane mycelium repo.
// The function exists only so cross-platform callers don't need
// build-tagged call sites.
func RaiseFileDescriptorLimit() (uint64, uint64, error) {
	return 0, 0, nil
}
