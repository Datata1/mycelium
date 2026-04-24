// Package gitref turns a git ref into the list of repo-relative paths
// changed between that ref and HEAD. Used by v1.6's `--since <ref>`
// filter so the query package can stay git-ignorant: transports run
// this at the boundary and pass the result into the reader as
// `pathsIn []string`.
//
// The three-dot form `<ref>...HEAD` asks git for the symmetric diff
// against the merge-base — which is what "files changed on my branch"
// usually means, even if HEAD has new merge commits from the base.
package gitref

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds the `git diff` invocation. Even on large repos
// `git diff --name-only` finishes in well under a second; 5s is
// defensive.
const DefaultTimeout = 5 * time.Second

// ResolveSince runs `git -C <root> diff --name-only <ref>...HEAD` and
// returns the resulting list of repo-relative paths. Returns an empty
// (non-nil) slice when the ref has no diff against HEAD — callers
// should distinguish nil (no filter) from empty ("no changes").
//
// Errors bubble up rather than silently resolving to empty: a failed
// git call should not look like "no matching files."
func ResolveSince(ctx context.Context, root, ref string) ([]string, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, errors.New("gitref: empty ref")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "git", "-C", root, "diff", "--name-only", ref+"...HEAD")
	out, err := cmd.Output()
	if err != nil {
		// exec.ExitError swallows stderr by default; surface it so
		// "unknown revision" / "not a git repo" reach the user.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			if stderr != "" {
				return nil, fmt.Errorf("gitref: %s", stderr)
			}
		}
		return nil, fmt.Errorf("gitref: %w", err)
	}

	// Guarantee a non-nil slice so callers can tell "no changes" from
	// "no filter" — an empty slice renders as `pathsIn = []`, which
	// the query package treats as the zero-row sentinel.
	paths := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		paths = append(paths, line)
	}
	return paths, nil
}
