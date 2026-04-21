package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	DefaultPath       = ".mycelium.yml"
	CurrentVersion    = 1
	DefaultSocket     = ".mycelium/daemon.sock"
	DefaultIndexPath  = ".mycelium/index.db"
	DefaultHTTPPort   = 7777
	DefaultDebounceMS = 200
	DefaultCoalesceMS = 2000
)

type Config struct {
	Version   int            `yaml:"version"`
	Languages []string       `yaml:"languages"`
	Include   []string       `yaml:"include"`
	Exclude   []string       `yaml:"exclude"`
	Embedder  EmbedderConfig `yaml:"embedder"`
	Chunking  ChunkingConfig `yaml:"chunking"`
	Watcher   WatcherConfig  `yaml:"watcher"`
	Daemon    DaemonConfig   `yaml:"daemon"`
	Hooks     HooksConfig    `yaml:"hooks"`
	Index     IndexConfig    `yaml:"index"`
}

type EmbedderConfig struct {
	Provider                 string `yaml:"provider"`      // none | ollama | voyage | openai
	Model                    string `yaml:"model"`
	Dimension                int    `yaml:"dimension"`
	Endpoint                 string `yaml:"endpoint"`
	APIKeyEnv                string `yaml:"api_key_env"`
	BatchSize                int    `yaml:"batch_size"`
	MaxConcurrency           int    `yaml:"max_concurrency"`
	RateLimitChunksPerMinute int    `yaml:"rate_limit_chunks_per_minute"`
}

type ChunkingConfig struct {
	SymbolMaxTokens         int  `yaml:"symbol_max_tokens"`
	IncludeDocstrings       bool `yaml:"include_docstrings"`
	FileFallbackWindowLines int  `yaml:"file_fallback_window_lines"`
}

type WatcherConfig struct {
	DebounceMS int `yaml:"debounce_ms"`
	CoalesceMS int `yaml:"coalesce_ms"`
}

type DaemonConfig struct {
	Socket   string `yaml:"socket"`
	HTTPPort int    `yaml:"http_port"`
}

type HooksConfig struct {
	PostCommit bool `yaml:"post_commit"`
}

type IndexConfig struct {
	Path           string `yaml:"path"`
	MaxFileSizeKB  int    `yaml:"max_file_size_kb"`
}

var supportedLanguages = map[string]bool{
	"go":         true,
	"typescript": true,
	"python":     true,
}

var supportedProviders = map[string]bool{
	"none":   true,
	"ollama": true,
	"voyage": true,
	"openai": true,
}

// Default returns a Config with sensible defaults for a freshly initialized repo.
func Default() Config {
	return Config{
		Version:   CurrentVersion,
		Languages: []string{"go", "typescript", "python"},
		Include:   []string{"**/*.go", "src/**/*.{ts,tsx}", "**/*.py"},
		Exclude: []string{
			"**/node_modules/**",
			"**/vendor/**",
			"**/dist/**",
			"**/build/**",
			"**/testdata/**",
			"**/*.generated.*",
			"**/*.min.js",
		},
		Embedder: EmbedderConfig{
			Provider:                 "none",
			BatchSize:                16,
			MaxConcurrency:           2,
			RateLimitChunksPerMinute: 2000,
		},
		Chunking: ChunkingConfig{
			SymbolMaxTokens:         1024,
			IncludeDocstrings:       true,
			FileFallbackWindowLines: 50,
		},
		Watcher: WatcherConfig{
			DebounceMS: DefaultDebounceMS,
			CoalesceMS: DefaultCoalesceMS,
		},
		Daemon: DaemonConfig{
			Socket:   DefaultSocket,
			HTTPPort: DefaultHTTPPort,
		},
		Hooks: HooksConfig{
			PostCommit: true,
		},
		Index: IndexConfig{
			Path:          DefaultIndexPath,
			MaxFileSizeKB: 1024,
		},
	}
}

// Load reads and validates a config file. Missing fields fall back to defaults.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks for known-bad configurations and returns an actionable error.
func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d (expected %d)", c.Version, CurrentVersion)
	}
	if len(c.Languages) == 0 {
		return fmt.Errorf("languages: at least one language is required")
	}
	for _, lang := range c.Languages {
		if !supportedLanguages[lang] {
			return fmt.Errorf("languages: %q is not supported (have: go, typescript, python)", lang)
		}
	}
	if !supportedProviders[c.Embedder.Provider] {
		return fmt.Errorf("embedder.provider: %q is not supported (have: none, ollama, voyage, openai)", c.Embedder.Provider)
	}
	if c.Embedder.Provider != "none" {
		if c.Embedder.Model == "" {
			return fmt.Errorf("embedder.model: required when provider is %q", c.Embedder.Provider)
		}
		if c.Embedder.Dimension <= 0 {
			return fmt.Errorf("embedder.dimension: must be > 0 when provider is %q", c.Embedder.Provider)
		}
	}
	if c.Daemon.HTTPPort < 0 || c.Daemon.HTTPPort > 65535 {
		return fmt.Errorf("daemon.http_port: %d out of range", c.Daemon.HTTPPort)
	}
	if c.Watcher.DebounceMS < 0 || c.Watcher.CoalesceMS < 0 {
		return fmt.Errorf("watcher: debounce and coalesce must be >= 0")
	}
	if c.Index.Path == "" {
		return fmt.Errorf("index.path: required")
	}
	return nil
}
