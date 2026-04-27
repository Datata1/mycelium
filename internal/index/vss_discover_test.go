package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBundledExtension_PrefersLibSubdir(t *testing.T) {
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	libPath := filepath.Join(libDir, "vec0.so")
	if err := os.WriteFile(libPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	flatPath := filepath.Join(dir, "vec0.so")
	if err := os.WriteFile(flatPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := resolveBundledExtension(dir, ".so")
	if got != libPath {
		t.Errorf("got %q, want %q (lib subdir takes precedence over flat layout)", got, libPath)
	}
}

func TestResolveBundledExtension_FallbackToFlatLayout(t *testing.T) {
	dir := t.TempDir()
	flatPath := filepath.Join(dir, "vec0.dylib")
	if err := os.WriteFile(flatPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := resolveBundledExtension(dir, ".dylib")
	if got != flatPath {
		t.Errorf("got %q, want %q (flat layout)", got, flatPath)
	}
}

func TestResolveBundledExtension_NoLibraryReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := resolveBundledExtension(dir, ".so"); got != "" {
		t.Errorf("got %q, want empty (no bundled library present)", got)
	}
}

func TestResolveBundledExtension_DirectoryAtCandidatePathIsIgnored(t *testing.T) {
	dir := t.TempDir()
	// Create a directory named "vec0.so" — must not be treated as a
	// loadable library (would crash sqlite-vec at runtime).
	bogus := filepath.Join(dir, "vec0.so")
	if err := os.MkdirAll(bogus, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := resolveBundledExtension(dir, ".so"); got != "" {
		t.Errorf("got %q, want empty (directory at candidate path)", got)
	}
}

func TestExtensionSuffix_KnownPlatforms(t *testing.T) {
	got := extensionSuffix()
	switch got {
	case ".so", ".dylib", ".dll":
		// runtime.GOOS resolved to one of the three known suffixes.
	default:
		t.Errorf("unexpected suffix %q on this platform", got)
	}
}
