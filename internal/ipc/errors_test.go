package ipc

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodeFor(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrNotFound, CodeNotFound},
		{ErrUnknownMethod, CodeUnknownMethod},
		{ErrBadParams, CodeBadParams},
		{fmt.Errorf("symbol %q: %w", "Foo", ErrNotFound), CodeNotFound},
		{fmt.Errorf("%w: json: cannot unmarshal", ErrBadParams), CodeBadParams},
		{errors.New("something else"), ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := CodeFor(c.err); got != c.want {
			t.Errorf("CodeFor(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestWireErrorIs(t *testing.T) {
	cases := []struct {
		code     string
		sentinel error
	}{
		{CodeNotFound, ErrNotFound},
		{CodeUnknownMethod, ErrUnknownMethod},
		{CodeBadParams, ErrBadParams},
	}
	for _, c := range cases {
		we := &wireError{msg: "daemon: boom", code: c.code}
		if !errors.Is(we, c.sentinel) {
			t.Errorf("wireError{code:%q} should match %v", c.code, c.sentinel)
		}
		for _, other := range cases {
			if other.sentinel != c.sentinel && errors.Is(we, other.sentinel) {
				t.Errorf("wireError{code:%q} must not match %v", c.code, other.sentinel)
			}
		}
	}
	if errors.Is(&wireError{msg: "x", code: ""}, ErrNotFound) {
		t.Error("codeless wireError must not match any sentinel")
	}
	if got := (&wireError{msg: "daemon: boom", code: CodeNotFound}).Error(); got != "daemon: boom" {
		t.Errorf("message not preserved verbatim: %q", got)
	}
}
