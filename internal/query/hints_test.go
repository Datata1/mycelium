package query

import (
	"strings"
	"testing"
)

func TestBuildFindHints(t *testing.T) {
	cases := []struct {
		name string
		in   findHintInput
		want [][]string // per expected line: substrings that must all appear
	}{
		{
			name: "no filters, no hints",
			in:   findHintInput{name: "Foo"},
			want: nil,
		},
		{
			name: "unknown project, none configured",
			in:   findHintInput{name: "Foo", project: "api", projectExists: false},
			want: [][]string{{`no project named "api"`, "single-project mode"}},
		},
		{
			name: "unknown project lists configured",
			in: findHintInput{
				name: "Foo", project: "api", projectExists: false,
				configuredProjects: []string{"web", "worker"},
			},
			want: [][]string{{"configured projects: [web, worker]"}},
		},
		{
			name: "existing project emits no project hint",
			in:   findHintInput{name: "Foo", project: "web", projectExists: true},
			want: nil,
		},
		{
			name: "kind filter eliminated real matches",
			in: findHintInput{
				name: "Login", kind: "type",
				matchedKinds: []string{"method", "function"},
			},
			want: [][]string{{`kind="type" eliminated them`, "[method, function]"}},
		},
		{
			name: "unknown kind entirely",
			in: findHintInput{
				name: "Login", kind: "clas",
				knownKinds: []string{"class", "function"},
			},
			want: [][]string{{`no symbols of kind "clas"`, "known kinds: [class, function]"}},
		},
		{
			name: "known kind but simply no match: no kind hint",
			in: findHintInput{
				name: "Nope", kind: "function",
				knownKinds: []string{"class", "function"},
			},
			want: nil,
		},
		{
			name: "project and kind hints combine",
			in: findHintInput{
				name: "Login", kind: "type", project: "api", projectExists: false,
				configuredProjects: []string{"web"},
				matchedKinds:       []string{"method"},
			},
			want: [][]string{{`no project named "api"`}, {`kind="type" eliminated them`}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildFindHints(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d hints %v, want %d", len(got), got, len(c.want))
			}
			for i, subs := range c.want {
				for _, sub := range subs {
					if !strings.Contains(got[i], sub) {
						t.Errorf("hint %d = %q, want substring %q", i, got[i], sub)
					}
				}
			}
		})
	}
}

func TestFormatList(t *testing.T) {
	if got := formatList(nil); got != "[]" {
		t.Errorf("empty: got %q", got)
	}
	if got := formatList([]string{"a", "b"}); got != "[a, b]" {
		t.Errorf("got %q", got)
	}
}
