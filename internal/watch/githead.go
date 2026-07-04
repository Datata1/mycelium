package watch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// headDebounce spaces HEAD ticks: git rewrites HEAD atomically via
// rename, but a checkout can touch it more than once (detach + attach).
const headDebounce = 500 * time.Millisecond

// StartHEADWatch watches <repoRoot>/.git for rewrites of the HEAD file —
// the one signal every branch switch produces — and emits a tick per
// checkout. It exists because the repo-tree watcher deliberately skips
// .git, and because git hooks don't fire when core.hooksPath points
// elsewhere (husky). The watch is non-recursive: fsnotify reports direct
// children of the directory only, which is exactly the noise level we
// want (HEAD, ORIG_HEAD, index — filtered by basename).
//
// Worktrees, where .git is a file containing `gitdir: <path>`, are
// resolved to the real git dir. Returns (nil, error) when the git dir
// can't be found or watched; callers treat that as non-fatal.
//
// The returned channel is buffered(1); ticks coalesce. It closes when
// ctx is cancelled.
func StartHEADWatch(ctx context.Context, repoRoot string, log *slog.Logger) (<-chan struct{}, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	gitDir, err := resolveGitDir(repoRoot)
	if err != nil {
		return nil, err
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("head watch: %w", err)
	}
	if err := fsw.Add(gitDir); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("head watch %s: %w", gitDir, err)
	}

	ticks := make(chan struct{}, 1)
	go func() {
		defer close(ticks)
		defer fsw.Close()
		var timer *time.Timer
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-fsw.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != "HEAD" {
					continue
				}
				if timer == nil {
					timer = time.AfterFunc(headDebounce, func() {
						select {
						case ticks <- struct{}{}:
						default:
						}
					})
				} else {
					timer.Reset(headDebounce)
				}
			case err, ok := <-fsw.Errors:
				if !ok {
					return
				}
				log.Warn("head watch error", "err", err)
			}
		}
	}()
	return ticks, nil
}

// resolveGitDir returns the directory holding HEAD for repoRoot: .git
// itself, or the `gitdir:` target when .git is a worktree link file.
func resolveGitDir(repoRoot string) (string, error) {
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", fmt.Errorf("head watch: %w", err)
	}
	if info.IsDir() {
		return gitPath, nil
	}
	b, err := os.ReadFile(gitPath)
	if err != nil {
		return "", fmt.Errorf("head watch: read .git file: %w", err)
	}
	line := strings.TrimSpace(string(b))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("head watch: .git file without gitdir: line")
	}
	dir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repoRoot, dir)
	}
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("head watch: gitdir %s: %w", dir, err)
	}
	return dir, nil
}
