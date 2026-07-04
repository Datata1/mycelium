package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultValidates(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default() must pass Validate(): %v", err)
	}
}

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), DefaultPath)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadOverridesDefaults(t *testing.T) {
	path := writeTempConfig(t, `
version: 1
languages: [go]
include: ["cmd/**/*.go"]
watcher:
  backend: watchman
  debounce_ms: 500
  coalesce_ms: 3000
telemetry:
  enabled: true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Languages, []string{"go"}) {
		t.Errorf("Languages = %v, want [go]", cfg.Languages)
	}
	if !reflect.DeepEqual(cfg.Include, []string{"cmd/**/*.go"}) {
		t.Errorf("Include = %v, want [cmd/**/*.go]", cfg.Include)
	}
	if cfg.Watcher.Backend != "watchman" {
		t.Errorf("Watcher.Backend = %q, want watchman", cfg.Watcher.Backend)
	}
	if cfg.Watcher.DebounceMS != 500 {
		t.Errorf("Watcher.DebounceMS = %d, want 500", cfg.Watcher.DebounceMS)
	}
	if cfg.Watcher.CoalesceMS != 3000 {
		t.Errorf("Watcher.CoalesceMS = %d, want 3000", cfg.Watcher.CoalesceMS)
	}
	if !cfg.Telemetry.Enabled {
		t.Error("Telemetry.Enabled = false, want true")
	}

	// Fields absent from the file must keep their defaults.
	def := Default()
	if !reflect.DeepEqual(cfg.Exclude, def.Exclude) {
		t.Errorf("Exclude = %v, want defaults %v", cfg.Exclude, def.Exclude)
	}
	if cfg.Daemon != def.Daemon {
		t.Errorf("Daemon = %+v, want defaults %+v", cfg.Daemon, def.Daemon)
	}
	if cfg.Index != def.Index {
		t.Errorf("Index = %+v, want defaults %+v", cfg.Index, def.Index)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("Load on a missing file must error")
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	path := writeTempConfig(t, "version: [not\n  closed: {")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load on malformed YAML must error")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse, got: %v", err)
	}
}

func TestLoadInvalidConfig(t *testing.T) {
	path := writeTempConfig(t, "version: 1\nlanguages: [cobol]\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with an unsupported language must error")
	}
	if !strings.Contains(err.Error(), "validate") {
		t.Errorf("error should mention validate, got: %v", err)
	}
}

func TestValidateFailures(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{
			name:    "wrong version",
			mutate:  func(c *Config) { c.Version = 99 },
			wantSub: "unsupported config version",
		},
		{
			name:    "no languages",
			mutate:  func(c *Config) { c.Languages = nil },
			wantSub: "at least one language",
		},
		{
			name:    "unknown language",
			mutate:  func(c *Config) { c.Languages = []string{"go", "cobol"} },
			wantSub: `"cobol" is not supported`,
		},
		{
			name:    "http port negative",
			mutate:  func(c *Config) { c.Daemon.HTTPPort = -1 },
			wantSub: "daemon.http_port",
		},
		{
			name:    "http port too large",
			mutate:  func(c *Config) { c.Daemon.HTTPPort = 70000 },
			wantSub: "daemon.http_port",
		},
		{
			name:    "negative debounce",
			mutate:  func(c *Config) { c.Watcher.DebounceMS = -1 },
			wantSub: "watcher: debounce and coalesce",
		},
		{
			name:    "negative coalesce",
			mutate:  func(c *Config) { c.Watcher.CoalesceMS = -1 },
			wantSub: "watcher: debounce and coalesce",
		},
		{
			name:    "unknown watcher backend",
			mutate:  func(c *Config) { c.Watcher.Backend = "kqueue" },
			wantSub: "watcher.backend",
		},
		{
			name:    "empty index path",
			mutate:  func(c *Config) { c.Index.Path = "" },
			wantSub: "index.path: required",
		},
		{
			name: "project missing name",
			mutate: func(c *Config) {
				c.Projects = []ProjectConfig{{Root: "svc"}}
			},
			wantSub: "projects[0].name: required",
		},
		{
			name: "project missing root",
			mutate: func(c *Config) {
				c.Projects = []ProjectConfig{{Name: "svc"}}
			},
			wantSub: "projects[0].root: required",
		},
		{
			name: "duplicate project names",
			mutate: func(c *Config) {
				c.Projects = []ProjectConfig{
					{Name: "svc", Root: "a"},
					{Name: "svc", Root: "b"},
				}
			},
			wantSub: `projects[1].name: duplicate "svc"`,
		},
		{
			name: "project unknown language",
			mutate: func(c *Config) {
				c.Projects = []ProjectConfig{
					{Name: "svc", Root: "a", Languages: []string{"rust"}},
				}
			},
			wantSub: `projects[0].languages: "rust" is not supported`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Validate() = %q, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidateAcceptsProjects(t *testing.T) {
	cfg := Default()
	cfg.Projects = []ProjectConfig{
		{Name: "api", Root: "services/api", Languages: []string{"go"}},
		{Name: "web", Root: "apps/web", Languages: []string{"typescript"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid workspace config rejected: %v", err)
	}
}

func TestApplyUserConfigZeroValueNoop(t *testing.T) {
	base := Default()
	got := ApplyUserConfig(base, UserConfig{})
	if !reflect.DeepEqual(got, base) {
		t.Errorf("empty UserConfig must not change base:\ngot  %+v\nwant %+v", got, base)
	}
}

func TestApplyUserConfigOverlays(t *testing.T) {
	u := UserConfig{
		Languages: []string{"python"},
		Exclude:   []string{"**/gen/**"},
		Watcher: WatcherConfig{
			Backend:         "watchman",
			DebounceMS:      50,
			CoalesceMS:      100,
			RescanThreshold: 250,
		},
		Daemon: DaemonConfig{
			Socket:   "/home/u/.myco/daemon.sock",
			HTTPPort: 8888,
		},
		Index: IndexConfig{
			Path:          "/home/u/.myco/index.db",
			MaxFileSizeKB: 2048,
		},
		Telemetry: TelemetryConfig{
			Enabled:       true,
			Path:          "/home/u/.myco/telemetry.jsonl",
			CharsPerToken: 3.5,
		},
	}
	got := ApplyUserConfig(Default(), u)

	if !reflect.DeepEqual(got.Languages, u.Languages) {
		t.Errorf("Languages = %v, want %v", got.Languages, u.Languages)
	}
	if !reflect.DeepEqual(got.Exclude, u.Exclude) {
		t.Errorf("Exclude = %v, want %v", got.Exclude, u.Exclude)
	}
	if got.Watcher != u.Watcher {
		t.Errorf("Watcher = %+v, want %+v", got.Watcher, u.Watcher)
	}
	if got.Daemon != u.Daemon {
		t.Errorf("Daemon = %+v, want %+v", got.Daemon, u.Daemon)
	}
	if got.Index != u.Index {
		t.Errorf("Index = %+v, want %+v", got.Index, u.Index)
	}
	if got.Telemetry != u.Telemetry {
		t.Errorf("Telemetry = %+v, want %+v", got.Telemetry, u.Telemetry)
	}

	// Fields UserConfig doesn't carry stay at base values.
	def := Default()
	if !reflect.DeepEqual(got.Include, def.Include) {
		t.Errorf("Include = %v, want base %v", got.Include, def.Include)
	}
	if got.Hooks != def.Hooks {
		t.Errorf("Hooks = %+v, want base %+v", got.Hooks, def.Hooks)
	}
}

func TestApplyUserConfigPartialOverlay(t *testing.T) {
	base := Default()
	got := ApplyUserConfig(base, UserConfig{
		Watcher: WatcherConfig{DebounceMS: 500},
	})
	if got.Watcher.DebounceMS != 500 {
		t.Errorf("Watcher.DebounceMS = %d, want 500", got.Watcher.DebounceMS)
	}
	if got.Watcher.CoalesceMS != base.Watcher.CoalesceMS {
		t.Errorf("Watcher.CoalesceMS = %d, want base %d", got.Watcher.CoalesceMS, base.Watcher.CoalesceMS)
	}
	if got.Watcher.Backend != base.Watcher.Backend {
		t.Errorf("Watcher.Backend = %q, want base %q", got.Watcher.Backend, base.Watcher.Backend)
	}
}

// Repo config wins over user config because loadRepoCtx applies
// ApplyUserConfig first and Load (the repo .mycelium.yml) last. Replaying
// that sequence here pins the precedence.
func TestRepoConfigWinsOverUserConfig(t *testing.T) {
	cfg := ApplyUserConfig(Default(), UserConfig{
		Languages: []string{"python"},
		Watcher:   WatcherConfig{DebounceMS: 50},
	})
	if !reflect.DeepEqual(cfg.Languages, []string{"python"}) {
		t.Fatalf("user overlay not applied: %v", cfg.Languages)
	}

	path := writeTempConfig(t, "version: 1\nlanguages: [go]\nwatcher:\n  debounce_ms: 900\n")
	repoCfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(repoCfg.Languages, []string{"go"}) {
		t.Errorf("Languages = %v, want repo value [go]", repoCfg.Languages)
	}
	if repoCfg.Watcher.DebounceMS != 900 {
		t.Errorf("Watcher.DebounceMS = %d, want repo value 900", repoCfg.Watcher.DebounceMS)
	}
}

func TestLoadUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("languages: [go]\ntelemetry:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	u, err := LoadUser(path)
	if err != nil {
		t.Fatalf("LoadUser: %v", err)
	}
	if !reflect.DeepEqual(u.Languages, []string{"go"}) {
		t.Errorf("Languages = %v, want [go]", u.Languages)
	}
	if !u.Telemetry.Enabled {
		t.Error("Telemetry.Enabled = false, want true")
	}
	// Unset fields stay at zero values (not defaults) so ApplyUserConfig
	// can tell "user set this" apart from "user left this empty".
	if u.Index.Path != "" {
		t.Errorf("Index.Path = %q, want zero value", u.Index.Path)
	}
	if u.Watcher.DebounceMS != 0 {
		t.Errorf("Watcher.DebounceMS = %d, want zero value", u.Watcher.DebounceMS)
	}
}

func TestLoadUserMalformedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("languages: [unclosed"), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	if _, err := LoadUser(path); err == nil {
		t.Fatal("LoadUser on malformed YAML must error")
	}
}
