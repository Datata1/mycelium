package check

import (
	"path"
	"strings"
)

// IsTestFile classifies a repo-relative path as a test file by naming
// convention, language-inferred from the extension:
//
//	go:     basename ends in _test.go
//	ts/js:  ".test." or ".spec." infix; any "__tests__" path segment
//	python: basename test_*.py or *_test.py; any "test"/"tests" segment
//
// Any "testdata" segment is never a test file — those are fixtures, not
// runnable tests.
func IsTestFile(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	segments := strings.Split(p, "/")
	for _, s := range segments[:len(segments)-1] {
		if s == "testdata" {
			return false
		}
	}
	base := path.Base(p)
	ext := strings.ToLower(path.Ext(base))

	switch ext {
	case ".go":
		return strings.HasSuffix(base, "_test.go")
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts":
		if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
			return true
		}
		for _, s := range segments[:len(segments)-1] {
			if s == "__tests__" {
				return true
			}
		}
		return false
	case ".py":
		stem := strings.TrimSuffix(base, ext)
		if strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test") {
			return true
		}
		for _, s := range segments[:len(segments)-1] {
			if s == "test" || s == "tests" {
				return true
			}
		}
		return false
	}
	return false
}
