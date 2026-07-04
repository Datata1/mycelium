package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepoDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	return root
}

func TestInstallAll_WritesEveryManagedHook(t *testing.T) {
	t.Parallel()
	root := gitRepoDir(t)

	installed, err := InstallAll(root)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(installed) != len(ManagedHooks) {
		t.Fatalf("installed %v, want all of %v", installed, ManagedHooks)
	}
	for _, name := range ManagedHooks {
		b, err := os.ReadFile(filepath.Join(root, ".git", "hooks", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !isMyceliumScript(b) {
			t.Errorf("%s: missing mycelium marker", name)
		}
		if !strings.Contains(string(b), "myco hook "+name) {
			t.Errorf("%s: script does not invoke `myco hook %s`:\n%s", name, name, b)
		}
	}
}

func TestInstallAll_NotAGitRepo(t *testing.T) {
	t.Parallel()
	installed, err := InstallAll(t.TempDir())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if installed != nil {
		t.Errorf("installed %v in a non-repo, want nil", installed)
	}
}

func TestInstallAll_BacksUpForeignHookAndUninstallRestoresIt(t *testing.T) {
	t.Parallel()
	root := gitRepoDir(t)
	foreign := "#!/bin/sh\necho user-owned\n"
	target := filepath.Join(root, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(target, []byte(foreign), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}

	if _, err := InstallAll(root); err != nil {
		t.Fatalf("install: %v", err)
	}
	backup, err := os.ReadFile(target + ".mycelium-backup")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != foreign {
		t.Errorf("backup content = %q, want the foreign script", backup)
	}

	removed, err := UninstallAll(root)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if len(removed) != len(ManagedHooks) {
		t.Errorf("removed %v, want all of %v", removed, ManagedHooks)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("hook gone after restore: %v", err)
	}
	if string(restored) != foreign {
		t.Errorf("restored content = %q, want the foreign script", restored)
	}
	if _, err := os.Stat(target + ".mycelium-backup"); !os.IsNotExist(err) {
		t.Errorf("backup file should be consumed by restore")
	}
	// The other hooks had no backup: plain delete.
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Errorf("post-commit should be deleted")
	}
}

func TestInstallAll_ReinstallOverOwnHookCreatesNoBackup(t *testing.T) {
	t.Parallel()
	root := gitRepoDir(t)
	if _, err := InstallAll(root); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := InstallAll(root); err != nil {
		t.Fatalf("second install: %v", err)
	}
	for _, name := range ManagedHooks {
		if _, err := os.Stat(filepath.Join(root, ".git", "hooks", name+".mycelium-backup")); !os.IsNotExist(err) {
			t.Errorf("%s: reinstall over our own script must not create a backup", name)
		}
	}
}

func TestUninstallAll_LeavesForeignHooksUntouched(t *testing.T) {
	t.Parallel()
	root := gitRepoDir(t)
	foreign := "#!/bin/sh\necho user-owned\n"
	target := filepath.Join(root, ".git", "hooks", "post-merge")
	if err := os.WriteFile(target, []byte(foreign), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}

	removed, err := UninstallAll(root)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed %v, want none (all hooks foreign or absent)", removed)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != foreign {
		t.Errorf("foreign hook modified: %q, %v", b, err)
	}
}
