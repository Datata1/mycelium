package hook

import (
	"fmt"
	"os"
	"path/filepath"
)

// ManagedHooks lists every git hook mycelium installs. All of them run
// the same daemon reindex ping; what differs is the git event covered:
// post-commit for commits, post-checkout for branch switches (the watcher
// can't see .git/HEAD and checkout bursts can overflow it), post-merge
// for merge/pull, post-rewrite for rebase/amend. Never pre-commit — myco
// must not sit in the user's commit path.
var ManagedHooks = []string{"post-commit", "post-checkout", "post-merge", "post-rewrite"}

// scriptFor renders the shell wrapper written into .git/hooks/<name>.
// It tolerates `myco` not being on PATH. Stdout/stderr are silenced and
// the call is backgrounded so git operations stay quick — errors land in
// the daemon log instead. Git hook arguments (e.g. post-checkout's
// <old> <new> <flag>) are deliberately not forwarded: every hook maps to
// the same full reconcile.
func scriptFor(name string) string {
	return fmt.Sprintf(`#!/bin/sh
# Managed by mycelium (myco init). Safe to edit; re-run myco init to restore.
if command -v myco >/dev/null 2>&1; then
  myco hook %s >/dev/null 2>&1 &
fi
exit 0
`, name)
}

// InstallAll writes every ManagedHooks script to <repoRoot>/.git/hooks/.
// If an existing foreign hook is present it is preserved via a
// .mycelium-backup suffix; a hook that is already ours is overwritten so
// installs stay current with script changes. Returns the names actually
// written; nil with no error means .git/hooks did not exist (not a git
// repo, or a worktree with a linked hooks dir).
func InstallAll(repoRoot string) ([]string, error) {
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	if _, err := os.Stat(hooksDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat hooks dir: %w", err)
	}
	var installed []string
	for _, name := range ManagedHooks {
		target := filepath.Join(hooksDir, name)
		if existing, err := os.ReadFile(target); err == nil && !isMyceliumScript(existing) {
			backup := target + ".mycelium-backup"
			if err := os.WriteFile(backup, existing, 0o755); err != nil {
				return installed, fmt.Errorf("back up existing %s hook: %w", name, err)
			}
		}
		if err := os.WriteFile(target, []byte(scriptFor(name)), 0o755); err != nil {
			return installed, fmt.Errorf("write %s hook: %w", name, err)
		}
		installed = append(installed, name)
	}
	return installed, nil
}

// UninstallAll removes every mycelium-managed hook. If a .mycelium-backup
// sibling exists it is restored in place; otherwise the hook is deleted.
// Foreign hooks (not managed by myco) are left untouched. Returns the
// names of hooks that were ours and have been removed or restored.
func UninstallAll(repoRoot string) ([]string, error) {
	var removed []string
	for _, name := range ManagedHooks {
		target := filepath.Join(repoRoot, ".git", "hooks", name)
		existing, err := os.ReadFile(target)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("read %s hook: %w", name, err)
		}
		if !isMyceliumScript(existing) {
			continue
		}
		backup := target + ".mycelium-backup"
		if b, err := os.ReadFile(backup); err == nil {
			if err := os.WriteFile(target, b, 0o755); err != nil {
				return removed, fmt.Errorf("restore %s hook backup: %w", name, err)
			}
			if err := os.Remove(backup); err != nil {
				return removed, fmt.Errorf("remove %s backup file: %w", name, err)
			}
			removed = append(removed, name)
			continue
		}
		if err := os.Remove(target); err != nil {
			return removed, fmt.Errorf("remove %s hook: %w", name, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}

func isMyceliumScript(content []byte) bool {
	return len(content) > 0 && containsBytes(content, []byte("Managed by mycelium"))
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
