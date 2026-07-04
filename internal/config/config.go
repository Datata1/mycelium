package config

import (
	"fmt"
	"os"
	"path/filepath"

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
	// DefaultRescanThreshold: 400 files in one coalesce window is well
	// past any hand-edit burst but below a typical branch-switch diff
	// on the repos where per-file churn starts to hurt.
	DefaultRescanThreshold = 400
)

type Config struct {
	Version   int           `yaml:"version"`
	Languages []string      `yaml:"languages"`
	Include   []string      `yaml:"include"`
	Exclude   []string      `yaml:"exclude"`
	Watcher   WatcherConfig `yaml:"watcher"`
	Daemon    DaemonConfig  `yaml:"daemon"`
	Hooks     HooksConfig   `yaml:"hooks"`
	Index     IndexConfig   `yaml:"index"`
	// Telemetry is v2.2's opt-in, local-only call-frequency log.
	// Default: disabled. When enabled, the daemon writes one JSON line
	// per dispatched IPC/MCP call to .mycelium/telemetry.jsonl. No
	// network. Drives `myco stats --telemetry` for adoption analysis.
	Telemetry TelemetryConfig `yaml:"telemetry"`
	// Projects is the v1.5 workspace mode. When empty, the whole repo
	// is one implicit "root project" indexed with the top-level
	// Languages/Include/Exclude — backward compatible with v1.4.
	// When non-empty, ONLY the listed sub-projects are indexed; the
	// top-level include/exclude are unused.
	Projects []ProjectConfig `yaml:"projects"`
}

// ProjectConfig scopes indexing to a sub-directory of the repo with its
// own include/exclude/languages.
type ProjectConfig struct {
	Name      string   `yaml:"name"`
	Root      string   `yaml:"root"`      // repo-relative
	Languages []string `yaml:"languages"` // optional; defaults to top-level
	Include   []string `yaml:"include"`
	Exclude   []string `yaml:"exclude"`
}

// WatcherConfig controls the file-watcher backend and debounce timing.
type WatcherConfig struct {
	Backend    string `yaml:"backend"` // "" / "fsnotify" (default) | "watchman" (v1.7)
	DebounceMS int    `yaml:"debounce_ms"`
	CoalesceMS int    `yaml:"coalesce_ms"`
	// RescanThreshold: a coalesce window carrying at least this many
	// file events (a branch switch, a stash pop) collapses into one
	// full rescan instead of per-file updates. 0 disables.
	RescanThreshold int `yaml:"rescan_threshold"`
}

type DaemonConfig struct {
	Socket   string `yaml:"socket"`
	HTTPPort int    `yaml:"http_port"`
}

type HooksConfig struct {
	PostCommit bool `yaml:"post_commit"`
}

// TelemetryConfig (v2.2). Off by default; opt-in. Kept narrow on purpose:
// the only knob is whether to log at all and where to put the file. We
// resist adding sampling rates, retention windows, or per-tool toggles
// until real usage shows we need them.
//
// CharsPerToken (v3.4 A2): byte→token conversion ratio for the session-
// cost estimate. 4.0 is a sensible default for English-heavy code+JSON
// flowing through Claude's tokenizer; users who benchmarked against
// their own tokenizer can override per repo. <= 0 falls back to the
// default at use time.
type TelemetryConfig struct {
	Enabled       bool    `yaml:"enabled"`
	Path          string  `yaml:"path"`            // empty -> .mycelium/telemetry.jsonl
	CharsPerToken float64 `yaml:"chars_per_token"` // 0 -> default 4.0; v3.4 A2
}

// DefaultCharsPerToken is the bytes→tokens conversion used when no
// override is configured. Exposed as a constant so the aggregator and
// the config layer don't disagree.
const DefaultCharsPerToken = 4.0

// IndexConfig controls the SQLite index location and per-file size limit.
type IndexConfig struct {
	Path          string `yaml:"path"`
	MaxFileSizeKB int    `yaml:"max_file_size_kb"`
}

var supportedLanguages = map[string]bool{
	"go":         true,
	"typescript": true,
	"python":     true,
}

// Default returns a Config with sensible defaults for a freshly initialized repo.
func Default() Config {
	return Config{
		Version:   CurrentVersion,
		Languages: []string{"go", "typescript", "python"},
		Include:   []string{"**/*.go", "**/*.{ts,tsx,d.ts,mts,cts}", "**/*.py"},
		Exclude: []string{
			"**/node_modules/**",
			"**/vendor/**",
			"**/dist/**",
			"**/build/**",
			"**/testdata/**",
			"**/*.generated.*",
			"**/*.min.js",
		},
		Watcher: WatcherConfig{
			DebounceMS:      DefaultDebounceMS,
			CoalesceMS:      DefaultCoalesceMS,
			RescanThreshold: DefaultRescanThreshold,
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
		Telemetry: TelemetryConfig{
			Enabled: false,
			Path:    "",
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
	if c.Daemon.HTTPPort < 0 || c.Daemon.HTTPPort > 65535 {
		return fmt.Errorf("daemon.http_port: %d out of range", c.Daemon.HTTPPort)
	}
	if c.Watcher.DebounceMS < 0 || c.Watcher.CoalesceMS < 0 {
		return fmt.Errorf("watcher: debounce and coalesce must be >= 0")
	}
	if c.Watcher.RescanThreshold < 0 {
		return fmt.Errorf("watcher.rescan_threshold: must be >= 0 (0 disables)")
	}
	switch c.Watcher.Backend {
	case "", "fsnotify", "watchman":
	default:
		return fmt.Errorf("watcher.backend: unknown value %q (fsnotify | watchman)", c.Watcher.Backend)
	}
	if c.Index.Path == "" {
		return fmt.Errorf("index.path: required")
	}
	// v1.5 workspace-mode validation. Names must be unique + non-empty;
	// roots must be non-empty. We don't enforce that Root exists on disk
	// — users may be generating projects from a template repo.
	seen := map[string]bool{}
	for i, p := range c.Projects {
		if p.Name == "" {
			return fmt.Errorf("projects[%d].name: required", i)
		}
		if p.Root == "" {
			return fmt.Errorf("projects[%d].root: required (use \".\" for repo root)", i)
		}
		if seen[p.Name] {
			return fmt.Errorf("projects[%d].name: duplicate %q", i, p.Name)
		}
		seen[p.Name] = true
		for _, lang := range p.Languages {
			if !supportedLanguages[lang] {
				return fmt.Errorf("projects[%d].languages: %q is not supported", i, lang)
			}
		}
	}
	return nil
}

// UserConfig is the subset of Config that makes sense as a user-level global
// default. It intentionally omits Projects (always repo-specific) and Include
// (too repo-specific). Index.Path and Daemon.Socket may hold ~/... paths set
// by `myco init --user` to store the index outside the repo.
type UserConfig struct {
	Version   int             `yaml:"version"`
	Languages []string        `yaml:"languages"`
	Exclude   []string        `yaml:"exclude"`
	Watcher   WatcherConfig   `yaml:"watcher"`
	Daemon    DaemonConfig    `yaml:"daemon"`
	Index     IndexConfig     `yaml:"index"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
}

// UserConfigPath returns the XDG-compliant user-level config path
// ($XDG_CONFIG_HOME/myco/config.yml, falling back to ~/.config/myco/config.yml).
// os.UserConfigDir() implements the XDG spec on Linux and platform equivalents
// on macOS/Windows.
func UserConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	return filepath.Join(dir, "myco", "config.yml"), nil
}

// LoadUser reads a UserConfig from path. Missing fields stay at zero values
// (not defaults) so applyUserConfig can distinguish "user set this" from "user
// left this empty".
func LoadUser(path string) (UserConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return UserConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var u UserConfig
	if err := yaml.Unmarshal(b, &u); err != nil {
		return UserConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return u, nil
}

// ApplyUserConfig overlays non-zero fields from u onto base. Slice fields
// (Languages, Exclude) replace the base slice when non-empty. Scalar fields
// replace the base value when non-zero. Repo-level .mycelium.yml always
// supersedes this because loadRepoCtx applies it last.
func ApplyUserConfig(base Config, u UserConfig) Config {
	if len(u.Languages) > 0 {
		base.Languages = u.Languages
	}
	if len(u.Exclude) > 0 {
		base.Exclude = u.Exclude
	}
	if u.Watcher.Backend != "" {
		base.Watcher.Backend = u.Watcher.Backend
	}
	if u.Watcher.DebounceMS != 0 {
		base.Watcher.DebounceMS = u.Watcher.DebounceMS
	}
	if u.Watcher.CoalesceMS != 0 {
		base.Watcher.CoalesceMS = u.Watcher.CoalesceMS
	}
	if u.Watcher.RescanThreshold != 0 {
		base.Watcher.RescanThreshold = u.Watcher.RescanThreshold
	}
	if u.Daemon.Socket != "" {
		base.Daemon.Socket = u.Daemon.Socket
	}
	if u.Daemon.HTTPPort != 0 {
		base.Daemon.HTTPPort = u.Daemon.HTTPPort
	}
	if u.Index.Path != "" {
		base.Index.Path = u.Index.Path
	}
	if u.Index.MaxFileSizeKB != 0 {
		base.Index.MaxFileSizeKB = u.Index.MaxFileSizeKB
	}
	if u.Telemetry.Enabled {
		base.Telemetry.Enabled = true
	}
	if u.Telemetry.Path != "" {
		base.Telemetry.Path = u.Telemetry.Path
	}
	if u.Telemetry.CharsPerToken != 0 {
		base.Telemetry.CharsPerToken = u.Telemetry.CharsPerToken
	}
	return base
}
