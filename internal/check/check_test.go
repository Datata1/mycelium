package check

import (
	"testing"

	"github.com/datata1/mycelium/internal/query"
)

func TestDiff(t *testing.T) {
	old := []OldSymbol{
		{Qualified: "pkg.Gone", Name: "Gone", Kind: "function", Path: "a.go"},
		{Qualified: "pkg.Moved", Name: "Moved", Kind: "function", Path: "a.go"},
		{Qualified: "pkg.Kept", Name: "Kept", Kind: "function", Path: "a.go"},
		{Qualified: "pkg.Gone", Name: "Gone", Kind: "function", Path: "b.go"}, // duplicate qualified
	}
	exists := map[string]struct{}{
		"pkg.Moved": {}, // moved within the diff — still defined somewhere
		"pkg.Kept":  {},
	}
	got := Diff(old, exists)
	if len(got) != 1 || got[0].Qualified != "pkg.Gone" {
		t.Fatalf("Diff = %+v, want exactly pkg.Gone", got)
	}
}

func TestClassify(t *testing.T) {
	removed := []Removed{
		{Qualified: "pkg.A", Name: "A"},
		{Qualified: "pkg.B", Name: "B"},
		{Qualified: "pkg.C", Name: "C"},
	}

	t.Run("exact_dangler_fails", func(t *testing.T) {
		danglers := []query.DanglingRef{
			{DstName: "pkg.A", DstShort: "A", SrcPath: "x.go", Line: 3, Exact: true},
		}
		out, level := Classify(removed, danglers)
		if level != LevelFail {
			t.Errorf("level = %s, want fail", level)
		}
		if !out[0].HasExact || len(out[0].Danglers) != 1 {
			t.Errorf("pkg.A classification wrong: %+v", out[0])
		}
	})

	t.Run("short_only_warns", func(t *testing.T) {
		danglers := []query.DanglingRef{
			{DstName: "other.B", DstShort: "B", SrcPath: "y.go", Line: 9, Exact: false},
		}
		out, level := Classify(removed, danglers)
		if level != LevelWarn {
			t.Errorf("level = %s, want warn", level)
		}
		if out[1].HasExact || len(out[1].Danglers) != 1 {
			t.Errorf("pkg.B classification wrong: %+v", out[1])
		}
	})

	t.Run("clean_deletion_passes", func(t *testing.T) {
		out, level := Classify(removed, nil)
		if level != LevelPass {
			t.Errorf("level = %s, want pass", level)
		}
		for _, c := range out {
			if len(c.Danglers) != 0 {
				t.Errorf("unexpected danglers: %+v", c)
			}
		}
	})

	t.Run("exact_sorts_before_short", func(t *testing.T) {
		danglers := []query.DanglingRef{
			{DstName: "x.C", DstShort: "C", SrcPath: "a.go", Line: 1, Exact: false},
			{DstName: "pkg.C", DstShort: "C", SrcPath: "z.go", Line: 5, Exact: true},
		}
		out, level := Classify(removed, danglers)
		if level != LevelFail {
			t.Errorf("level = %s, want fail", level)
		}
		if !out[2].Danglers[0].Exact {
			t.Errorf("expected exact dangler first: %+v", out[2].Danglers)
		}
	})
}

func TestIsTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"internal/query/check_test.go", true},
		{"internal/query/check.go", false},
		{"src/utils/plans.test.ts", true},
		{"src/utils/plans.spec.tsx", true},
		{"src/__tests__/plans.ts", true},
		{"src/utils/plans.ts", false},
		{"pkg/test_config.py", true},
		{"pkg/config_test.py", true},
		{"tests/config.py", true},
		{"pkg/config.py", false},
		{"testdata/fixtures/a_test.go", false},
		{"test/integration/get_refs_test.go", true},
		{"docs/readme.md", false},
	}
	for _, tc := range cases {
		if got := IsTestFile(tc.path); got != tc.want {
			t.Errorf("IsTestFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
