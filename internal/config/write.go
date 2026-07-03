package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Write marshals cfg to path, overwriting any existing file.
func Write(path string, cfg Config) error {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	preamble := []byte("# Mycelium configuration. Edit to suit your repo.\n" +
		"# See https://github.com/datata1/mycelium for the full schema.\n")
	return os.WriteFile(path, append(preamble, out...), 0o644)
}

// WriteUser marshals u to path, creating parent directories as needed.
func WriteUser(path string, u UserConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	out, err := yaml.Marshal(u)
	if err != nil {
		return fmt.Errorf("marshal user config: %w", err)
	}
	preamble := []byte("# Mycelium user-level config. Per-repo .mycelium.yml always takes priority.\n" +
		"# See https://github.com/datata1/mycelium for the full schema.\n")
	return os.WriteFile(path, append(preamble, out...), 0o644)
}

// WriteDefault writes the annotated default config to path. Fails if the file
// already exists; callers should check first and decide whether to overwrite.
func WriteDefault(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	cfg := Default()
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	preamble := []byte(`# Mycelium configuration. Edit to suit your repo.
# See https://github.com/datata1/mycelium for the full schema.
`)
	if err := os.WriteFile(path, append(preamble, out...), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
