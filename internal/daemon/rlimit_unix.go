//go:build !windows

package daemon

import "syscall"

// RaiseFileDescriptorLimit bumps the process's soft RLIMIT_NOFILE to
// the hard limit (or the highest value the kernel actually accepts)
// at daemon startup. Returns (newSoft, hard, err); the daemon logs
// the change to stderr so users see it.
//
// Why this matters for v4: the v4 F1/T2 finding hit `EMFILE`
// ("too many open files") on Codesphere monorepo-4 because the
// daemon's fsnotify watcher consumes one fd per watched directory,
// and macOS's default RLIMIT_NOFILE soft limit is 256. The hard
// limit is typically 10240 — bumping the soft to hard at startup
// gives the daemon ~40× more headroom for free, no user action
// required. Linux defaults are usually 1024 / 1048576, so the bump
// is even more dramatic there.
//
// macOS quirk: Setrlimit to the apparent hard limit can fail because
// the kernel cap `kern.maxfilesperproc` (default 24576) is sometimes
// lower than the per-process hard limit. We try the hard limit first;
// if that fails, step down through a sequence of known-safe values
// (10240, 4096, 2048) and accept whichever the kernel allows. Returns
// gracefully on any fatal error so a permission-denied setrlimit
// doesn't crash the daemon at startup.
func RaiseFileDescriptorLimit() (newSoft uint64, hard uint64, err error) {
	var rlim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlim); err != nil {
		return 0, 0, err
	}
	hard = uint64(rlim.Max)
	if rlim.Cur >= rlim.Max {
		return uint64(rlim.Cur), hard, nil
	}

	// First attempt: bump straight to hard.
	target := rlim
	target.Cur = rlim.Max
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &target); err == nil {
		return uint64(target.Cur), hard, nil
	}

	// macOS fallback: step down through plausible values and accept
	// whichever the kernel takes. Each retry is one syscall, so a few
	// failures on startup cost microseconds.
	for _, attempt := range []uint64{10240, 4096, 2048, 1024} {
		if attempt <= uint64(rlim.Cur) {
			break // already at or above this attempt; no improvement
		}
		// rlim.Cur is uint64 on Linux+macOS, int64 on FreeBSD; the
		// explicit conversion keeps the build green across all three
		// without per-OS build tags.
		target.Cur = uint64(attempt)
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &target); err == nil {
			return attempt, hard, nil
		}
	}
	return uint64(rlim.Cur), hard, nil
}
