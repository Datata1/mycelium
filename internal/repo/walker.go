package repo

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Walker enumerates files under a root that match include patterns and are
// not excluded. v0.1 does not parse .gitignore; Exclude patterns are expected
// to cover vendored/build dirs via the default config.
type Walker struct {
	Root           string
	Include        []string
	Exclude        []string
	MaxFileSizeKB  int
	AlwaysSkipDirs map[string]bool
}

// File is one result from the walker.
//
// ProjectID is 0 for files that don't belong to any explicit project
// (the implicit "root project" when .mycelium.yml has no projects:
// list) and the project row id otherwise. Caller fills it in after the
// walk — the Walker itself is project-agnostic.
type File struct {
	AbsPath   string
	RelPath   string // forward-slash, repo-relative
	SizeKB    int
	MTimeNS   int64
	ProjectID int64
}

func NewWalker(root string, include, exclude []string, maxFileSizeKB int) *Walker {
	return &Walker{
		Root:          root,
		Include:       include,
		Exclude:       exclude,
		MaxFileSizeKB: maxFileSizeKB,
		AlwaysSkipDirs: map[string]bool{
			".git":      true,
			".mycelium": true,
		},
	}
}

// Walk yields every matching file. Any error walking the tree is returned as-is.
func (w *Walker) Walk() ([]File, error) {
	root, err := filepath.Abs(w.Root)
	if err != nil {
		return nil, fmt.Errorf("abs root: %w", err)
	}
	var out []File
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if w.AlwaysSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		if !w.includeMatches(rel) {
			return nil
		}
		if w.excludeMatches(rel) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		sizeKB := int(info.Size() / 1024)
		if w.MaxFileSizeKB > 0 && sizeKB > w.MaxFileSizeKB {
			return nil
		}
		out = append(out, File{
			AbsPath: path,
			RelPath: rel,
			SizeKB:  sizeKB,
			MTimeNS: info.ModTime().UnixNano(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

func (w *Walker) includeMatches(rel string) bool {
	if len(w.Include) == 0 {
		return true
	}
	for _, pat := range w.Include {
		if doublestarMatch(pat, rel) {
			return true
		}
	}
	return false
}

func (w *Walker) excludeMatches(rel string) bool {
	for _, pat := range w.Exclude {
		if doublestarMatch(pat, rel) {
			return true
		}
	}
	return false
}

// DoublestarMatch implements a small subset of glob semantics that filepath.Match
// lacks:
//   - "**" matches any sequence of path segments, including zero.
//   - Brace expansion: "{a,b}" -> "a" or "b" (single level, no nesting).
//
// Exported so other packages (the watcher) can use the same matching rules.
// Avoids a dependency on github.com/bmatcuk/doublestar for v0.2. Swap in
// the full library if requirements grow.
func DoublestarMatch(pattern, name string) bool { return doublestarMatch(pattern, name) }

func doublestarMatch(pattern, name string) bool {
	for _, expanded := range expandBraces(pattern) {
		if matchSegments(expanded, name) {
			return true
		}
	}
	return false
}

func expandBraces(pattern string) []string {
	lb := strings.IndexByte(pattern, '{')
	if lb < 0 {
		return []string{pattern}
	}
	rb := strings.IndexByte(pattern[lb:], '}')
	if rb < 0 {
		return []string{pattern}
	}
	rb += lb
	prefix := pattern[:lb]
	suffix := pattern[rb+1:]
	options := strings.Split(pattern[lb+1:rb], ",")
	var out []string
	for _, opt := range options {
		out = append(out, expandBraces(prefix+opt+suffix)...)
	}
	return out
}

func matchSegments(pattern, name string) bool {
	pSegs := strings.Split(pattern, "/")
	nSegs := strings.Split(name, "/")
	return matchSegs(pSegs, nSegs)
}

func matchSegs(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			if len(p) == 1 {
				return true
			}
			// Try matching the rest of the pattern at every suffix of n.
			for i := 0; i <= len(n); i++ {
				if matchSegs(p[1:], n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		ok, err := filepath.Match(p[0], n[0])
		if err != nil || !ok {
			return false
		}
		p = p[1:]
		n = n[1:]
	}
	return len(n) == 0
}

// DiscoverRoot walks up from start looking for a .mycelium.yml or .git directory.
// Returns the directory containing it, or start if nothing is found.
func DiscoverRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	dir := abs
	for {
		if fileExists(filepath.Join(dir, ".mycelium.yml")) {
			return dir, nil
		}
		if dirExists(filepath.Join(dir, ".git")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		dir = parent
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
