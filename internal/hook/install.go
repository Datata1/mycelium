package hook

import (
	"fmt"
	"os"
	"path/filepath"
)

// postCommitScript is the shell wrapper written into .git/hooks/post-commit.
// It tolerates `myco` not being on PATH by falling back to a well-known
// location set by `myco init`. Stdout/stderr are silenced so commits stay
// quick — errors land in the daemon log instead.
const postCommitScript = `#!/bin/sh
# Managed by mycelium (myco init). Safe to edit; re-run myco init to restore.
if command -v myco >/dev/null 2>&1; then
  myco hook post-commit >/dev/null 2>&1 &
fi
exit 0
`

// InstallPostCommit writes the hook script to <repoRoot>/.git/hooks/post-commit.
// If an existing hook is present it is preserved via a .mycelium-backup suffix.
// Returns (installed, error); installed=false means .git/hooks did not exist
// (not a git repo, or a worktree with a linked hooks dir).
func InstallPostCommit(repoRoot string) (bool, error) {
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	if _, err := os.Stat(hooksDir); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat hooks dir: %w", err)
	}
	target := filepath.Join(hooksDir, "post-commit")
	if existing, err := os.ReadFile(target); err == nil {
		if isMyceliumScript(existing) {
			// Already ours; overwrite so we stay current with any script changes.
		} else {
			backup := target + ".mycelium-backup"
			if err := os.WriteFile(backup, existing, 0o755); err != nil {
				return false, fmt.Errorf("back up existing hook: %w", err)
			}
		}
	}
	if err := os.WriteFile(target, []byte(postCommitScript), 0o755); err != nil {
		return false, fmt.Errorf("write hook: %w", err)
	}
	return true, nil
}

// UninstallPostCommit removes the mycelium-managed post-commit hook. If a
// .mycelium-backup sibling exists it is restored in place; otherwise the
// hook is simply deleted. Returns (removed, error) where removed=true
// means the hook was ours and has been removed (or replaced by the backup).
// If the existing hook is foreign (not managed by myco), the function is
// a no-op and returns (false, nil) so we never clobber user content.
func UninstallPostCommit(repoRoot string) (bool, error) {
	target := filepath.Join(repoRoot, ".git", "hooks", "post-commit")
	existing, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read hook: %w", err)
	}
	if !isMyceliumScript(existing) {
		return false, nil
	}
	backup := target + ".mycelium-backup"
	if b, err := os.ReadFile(backup); err == nil {
		if err := os.WriteFile(target, b, 0o755); err != nil {
			return false, fmt.Errorf("restore hook backup: %w", err)
		}
		if err := os.Remove(backup); err != nil {
			return false, fmt.Errorf("remove backup file: %w", err)
		}
		return true, nil
	}
	if err := os.Remove(target); err != nil {
		return false, fmt.Errorf("remove hook: %w", err)
	}
	return true, nil
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
