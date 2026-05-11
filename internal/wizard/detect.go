// Package wizard implements the interactive setup wizard for myco init.
package wizard

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// skipDirs are directory names that are never interesting for detection.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".git":         true,
	".mycelium":    true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	".tox":         true,
	"target":       true, // Rust/Maven
	"out":          true,
	".next":        true,
	".nuxt":        true,
	"coverage":     true,
}

// LangHit records how many files of each language were found.
type LangHit struct {
	Language string
	Count    int
}

// Subproject is a detected monorepo sub-package.
type Subproject struct {
	// RelDir is the directory relative to the repo root (e.g. "services/api").
	RelDir string
	// MarkerFile is the file that triggered detection ("go.mod", "package.json", …).
	MarkerFile string
	// SuggestedName is the last path component, offered as the default project name.
	SuggestedName string
}

// subprojectMarkers maps marker filenames to the language they indicate.
var subprojectMarkers = map[string]string{
	"go.mod":          "go",
	"package.json":    "typescript",
	"pyproject.toml":  "python",
	"setup.py":        "python",
	"setup.cfg":       "python",
}

// DetectLanguages walks the repo tree and returns which languages have
// source files present, ordered by file count descending. The walk
// respects skipDirs and stops counting each language after 2000 files
// (presence is all we need).
func DetectLanguages(root string) ([]LangHit, error) {
	counts := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		lang := langFromExt(d.Name())
		if lang != "" && counts[lang] < 2000 {
			counts[lang]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Return only languages with at least one file, sorted by count.
	var out []LangHit
	for _, lang := range []string{"go", "typescript", "python"} {
		if n := counts[lang]; n > 0 {
			out = append(out, LangHit{Language: lang, Count: n})
		}
	}
	return out, nil
}

// DetectSubprojects walks the full repo tree and returns every
// directory that contains a subproject marker (go.mod, package.json,
// pyproject.toml) that is NOT the repo root itself. Used to propose
// workspace projects in the wizard.
func DetectSubprojects(root string) ([]Subproject, error) {
	var out []Subproject
	seen := map[string]bool{} // prevent duplicate dirs from multiple markers

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		_, isMarker := subprojectMarkers[d.Name()]
		if !isMarker {
			return nil
		}
		dir := filepath.Dir(path)
		relDir, err := filepath.Rel(root, dir)
		if err != nil || relDir == "." {
			return nil // skip root-level markers
		}
		if seen[relDir] {
			return nil
		}
		seen[relDir] = true
		out = append(out, Subproject{
			RelDir:        relDir,
			MarkerFile:    d.Name(),
			SuggestedName: SuggestProjectName(relDir),
		})
		return nil
	})
	return out, err
}

// genericNames are base directory names so common they convey no unique
// identity on their own. When detected, the parent directory is prepended.
// Deliberately narrow: service-level names (api, web, server…) are kept
// as-is because they are usually unique within a repo.
var genericNames = map[string]bool{
	"common": true, "shared": true,
	"lib": true, "libs": true,
	"core": true, "node": true,
	"utils": true, "util": true,
	"pkg": true, "src": true,
}

// SuggestProjectName returns a project name for a sub-directory. When
// the base directory name is generic (common, node, shared, …) it
// prepends the parent component so xxx-service/common → xxx-service-common
// instead of the ambiguous "common".
func SuggestProjectName(relDir string) string {
	parts := strings.Split(filepath.ToSlash(relDir), "/")
	base := parts[len(parts)-1]
	if genericNames[strings.ToLower(base)] && len(parts) >= 2 {
		parent := parts[len(parts)-2]
		// Strip common suffixes from parent to keep names concise.
		parent = strings.TrimSuffix(parent, "-service")
		parent = strings.TrimSuffix(parent, "-svc")
		return parent + "-" + base
	}
	return base
}

// langFromExt maps a filename to a mycelium language identifier.
func langFromExt(name string) string {
	switch {
	case strings.HasSuffix(name, ".go"):
		return "go"
	case strings.HasSuffix(name, ".ts"),
		strings.HasSuffix(name, ".tsx"),
		strings.HasSuffix(name, ".js"),
		strings.HasSuffix(name, ".jsx"):
		return "typescript"
	case strings.HasSuffix(name, ".py"):
		return "python"
	}
	return ""
}

// HomeDir returns the user's home directory. Used by MCP detection.
func HomeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}
