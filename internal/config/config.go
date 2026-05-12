// Package config loads, validates, and persists the TOML configuration
// that drives every other subsystem. Load merges Defaults with the file
// at the given path, expands "~/" prefixes, and overlays a small set of
// environment variables (TURSO_*, DOTFILES_LOG_BACKEND). The resulting
// *Config can be attached to a context via WithContext / FromContext.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the fully-resolved TOML configuration used throughout the
// application.
type Config struct {
	Repo   RepoConfig   `toml:"repo"`
	AI     AIConfig     `toml:"ai"`
	Log    LogConfig    `toml:"log"`
	State  StateConfig  `toml:"state"`
	Backup BackupConfig `toml:"backup"`
}

// BackupConfig controls the snapshot blob store: where blobs live on
// disk, the total size cap (MB), and the retention horizon (days).
type BackupConfig struct {
	Dir           string `toml:"dir"`
	MaxTotalMB    int64  `toml:"max_total_mb"`
	RetentionDays int    `toml:"retention_days"`
}

// RepoConfig is the location of the backup git repo. Local is the
// working clone on disk; Remote is the origin URL (may be empty).
type RepoConfig struct {
	Remote string `toml:"remote"`
	Local  string `toml:"local"`
}

// AIConfig selects which AI provider to use and carries its settings.
type AIConfig struct {
	Provider   string           `toml:"provider"`
	ClaudeCode ClaudeCodeConfig `toml:"claude-code"`
}

// ClaudeCodeConfig configures the claude-code adapter: which binary to
// invoke, which model to request, and any extra CLI args to forward.
type ClaudeCodeConfig struct {
	Bin       string   `toml:"bin"`
	Model     string   `toml:"model"`
	ExtraArgs []string `toml:"extra_args"`
}

// LogConfig configures the audit log: Path is the JSONL destination,
// Backend selects "both" | "jsonl" | "db" | "none".
type LogConfig struct {
	Path    string `toml:"path"`
	Backend string `toml:"backend"`
}

var knownLogBackends = map[string]bool{
	"both":  true,
	"jsonl": true,
	"db":    true,
	"none":  true,
}

// StateConfig points the application at its libSQL/Turso database. URL
// may be a file:// path or a libsql:// / https:// remote URL; AuthToken
// is required only for remote URLs.
type StateConfig struct {
	URL       string `toml:"url"`
	AuthToken string `toml:"auth_token,omitempty"`
}

var knownProviders = map[string]bool{
	"claude-code": true,
}

type ctxKey struct{}

// WithContext returns a derived context that carries c. Use FromContext
// to retrieve it deeper in the call stack.
func WithContext(ctx context.Context, c *Config) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext returns the *Config previously attached with WithContext,
// or nil if none is present.
func FromContext(ctx context.Context) *Config {
	if c, ok := ctx.Value(ctxKey{}).(*Config); ok {
		return c
	}
	return nil
}

func xdgConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func xdgDataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// DefaultPath returns the canonical config file location under
// $XDG_CONFIG_HOME/dotfiles/config.toml.
func DefaultPath() (string, error) {
	return filepath.Join(xdgConfigHome(), "dotfiles", "config.toml"), nil
}

func expandHome(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	if strings.HasPrefix(p, "file://~/") {
		home, _ := os.UserHomeDir()
		return "file://" + filepath.Join(home, p[len("file://~/"):])
	}
	if strings.HasPrefix(p, "file:///~/") {
		home, _ := os.UserHomeDir()
		return "file://" + filepath.Join(home, p[len("file:///~/"):])
	}
	return p
}

// Defaults returns a *Config populated with reasonable XDG-based paths
// and sensible defaults for every section. Load starts from these.
func Defaults() *Config {
	data := xdgDataHome()
	return &Config{
		Repo: RepoConfig{
			Local: filepath.Join(data, "dotfiles", "repo"),
		},
		AI: AIConfig{
			Provider: "claude-code",
			ClaudeCode: ClaudeCodeConfig{
				Bin:       "claude",
				ExtraArgs: []string{},
			},
		},
		Log: LogConfig{
			Path:    filepath.Join(data, "dotfiles", "logs", "actions.jsonl"),
			Backend: "both",
		},
		State: StateConfig{
			URL: "file://" + filepath.Join(data, "dotfiles", "state.db"),
		},
		Backup: BackupConfig{
			Dir:           filepath.Join(data, "dotfiles", "backups"),
			MaxTotalMB:    500,
			RetentionDays: 90,
		},
	}
}

// Load reads a TOML config from path on top of Defaults, expands "~/"
// prefixes, and applies the TURSO_DATABASE_URL, TURSO_AUTH_TOKEN, and
// DOTFILES_LOG_BACKEND environment overrides. A missing file is not an
// error; Defaults are returned. An empty path skips file decoding.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if _, err := toml.DecodeFile(path, cfg); err != nil {
				return nil, fmt.Errorf("decode %s: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
	}

	cfg.Repo.Local = expandHome(cfg.Repo.Local)
	cfg.Log.Path = expandHome(cfg.Log.Path)
	cfg.State.URL = expandHome(cfg.State.URL)
	cfg.Backup.Dir = expandHome(cfg.Backup.Dir)

	if v := os.Getenv("TURSO_DATABASE_URL"); v != "" {
		cfg.State.URL = v
	}
	if v := os.Getenv("TURSO_AUTH_TOKEN"); v != "" {
		cfg.State.AuthToken = v
	}
	if v := os.Getenv("DOTFILES_LOG_BACKEND"); v != "" {
		cfg.Log.Backend = strings.ToLower(v)
	}
	if cfg.Log.Backend == "" {
		cfg.Log.Backend = "both"
	}

	return cfg, nil
}

// Save writes cfg as TOML to path, creating parent directories as
// needed.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	enc := toml.NewEncoder(f)
	if err := enc.Encode(cfg); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// Validate reports an error if cfg.AI.Provider or cfg.Log.Backend is
// not one of the known values.
func (c *Config) Validate() error {
	if !knownProviders[c.AI.Provider] {
		return fmt.Errorf("unknown ai.provider %q", c.AI.Provider)
	}
	if !knownLogBackends[c.Log.Backend] {
		return fmt.Errorf("unknown log.backend: %q (allowed: both|jsonl|db|none)", c.Log.Backend)
	}
	return nil
}

// EncodeTOML returns c serialized to TOML bytes.
func (c *Config) EncodeTOML() ([]byte, error) {
	var sb strings.Builder
	if err := toml.NewEncoder(&sb).Encode(c); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}
