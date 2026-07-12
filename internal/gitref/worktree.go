// Worktree-aware companions to ResolveSince, used by the verifier
// (`myco check` / `verify_changes`). Where ResolveSince deliberately
// looks at committed history only (three-dot merge-base diff, the
// `--since` query-filter semantic), a verifier must also see staged and
// unstaged edits — the whole point is checking work the agent just did.
package gitref

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ChangedSince resolves base = `git merge-base <ref> HEAD` (for
// ref=HEAD the base is HEAD itself) and returns the paths changed
// between base and the working tree: committed-on-branch plus staged
// and unstaged tracked changes. `--no-renames` splits renames into a
// deletion + addition so the old path's symbols participate in
// removed-symbol detection. Untracked new files are invisible (they
// cannot remove symbols).
//
// Returns a non-nil empty slice for a clean tree; errors bubble up in
// the ResolveSince style (stderr surfaced).
func ChangedSince(ctx context.Context, root, ref string) (base string, paths []string, err error) {
	if strings.TrimSpace(ref) == "" {
		return "", nil, errors.New("gitref: empty ref")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	baseOut, err := gitOutput(ctx, root, "merge-base", ref, "HEAD")
	if err != nil {
		return "", nil, err
	}
	base = strings.TrimSpace(string(baseOut))
	if base == "" {
		return "", nil, fmt.Errorf("gitref: merge-base of %q and HEAD is empty", ref)
	}

	diffOut, err := gitOutput(ctx, root, "diff", "--name-only", "--no-renames", base)
	if err != nil {
		return "", nil, err
	}
	paths = []string{}
	for _, line := range strings.Split(string(diffOut), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		paths = append(paths, line)
	}
	return base, paths, nil
}

// ShowAtCommit returns the blob content of path at commit. ok=false
// (with nil error) means the path did not exist at that commit — the
// caller treats the file as newly added, so it has no old symbols.
func ShowAtCommit(ctx context.Context, root, commit, path string) (content []byte, ok bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "git", "-C", root, "show", commit+":"+path)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			// "does not exist" / "exists on disk, but not in <commit>":
			// the path is new relative to the base commit.
			if bytes.Contains([]byte(stderr), []byte("does not exist")) ||
				bytes.Contains([]byte(stderr), []byte("exists on disk, but not in")) {
				return nil, false, nil
			}
			if stderr != "" {
				return nil, false, fmt.Errorf("gitref: %s", stderr)
			}
		}
		return nil, false, fmt.Errorf("gitref: %w", err)
	}
	return out, true, nil
}

// gitOutput runs one git subcommand rooted at root, surfacing stderr on
// failure (same error contract as ResolveSince).
func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			if stderr != "" {
				return nil, fmt.Errorf("gitref: %s", stderr)
			}
		}
		return nil, fmt.Errorf("gitref: %w", err)
	}
	return out, nil
}
