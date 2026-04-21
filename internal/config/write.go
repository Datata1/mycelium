package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

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
# See https://github.com/jdwiederstein/mycelium for the full schema.
`)
	if err := os.WriteFile(path, append(preamble, out...), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
