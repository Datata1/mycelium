package index

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultExtensionPath returns the path to a bundled sqlite-vec
// shared library next to the running binary, or "" if no bundled
// library is present. Used by callers (cmd/myco) when the user
// hasn't set `index.vector.extension_path` in `.mycelium.yml` —
// release tarballs ship the library at `lib/vec0.{so,dylib,dll}`
// next to the `myco` binary, so a fresh install gets the vec0 fast
// path without any extra config.
//
// Resolution order:
//
//   1. <exe-dir>/lib/vec0.<ext>  — release tarball layout.
//   2. <exe-dir>/vec0.<ext>      — same dir as the binary, in case
//                                  someone unpacked the archive flat.
//
// Anything not present at one of those paths returns "" and the
// caller falls back to the brute-force cosine path. We never look
// in /usr/local or other system locations: the user's config is
// the authoritative override and a discovered system library
// would be surprising.
func DefaultExtensionPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	libName := "vec0" + extensionSuffix()
	for _, candidate := range []string{
		filepath.Join(dir, "lib", libName),
		filepath.Join(dir, libName),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// extensionSuffix returns the platform-specific shared-library
// extension. sqlite-vec releases follow these conventions: `.so` on
// Linux, `.dylib` on macOS, `.dll` on Windows.
func extensionSuffix() string {
	switch runtime.GOOS {
	case "darwin":
		return ".dylib"
	case "windows":
		return ".dll"
	default:
		return ".so"
	}
}
