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

type Config struct {
	Repo   RepoConfig   `toml:"repo"`
	AI     AIConfig     `toml:"ai"`
	Log    LogConfig    `toml:"log"`
	State  StateConfig  `toml:"state"`
	Backup BackupConfig `toml:"backup"`
}

type BackupConfig struct {
	Dir           string `toml:"dir"`
	MaxTotalMB    int64  `toml:"max_total_mb"`
	RetentionDays int    `toml:"retention_days"`
}

type RepoConfig struct {
	Remote string `toml:"remote"`
	Local  string `toml:"local"`
}

type AIConfig struct {
	Provider   string           `toml:"provider"`
	ClaudeCode ClaudeCodeConfig `toml:"claude-code"`
}

type ClaudeCodeConfig struct {
	Bin       string   `toml:"bin"`
	Model     string   `toml:"model"`
	ExtraArgs []string `toml:"extra_args"`
}

type LogConfig struct {
	Path string `toml:"path"`
}

type StateConfig struct {
	URL       string `toml:"url"`
	AuthToken string `toml:"auth_token,omitempty"`
}

var knownProviders = map[string]bool{
	"claude-code": true,
}

type ctxKey struct{}

func WithContext(ctx context.Context, c *Config) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

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
			Path: filepath.Join(data, "dotfiles", "logs", "actions.jsonl"),
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

	return cfg, nil
}

func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	enc := toml.NewEncoder(f)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return nil
}

func (c *Config) Validate() error {
	if !knownProviders[c.AI.Provider] {
		return fmt.Errorf("unknown ai.provider %q", c.AI.Provider)
	}
	return nil
}

func (c *Config) EncodeTOML() ([]byte, error) {
	var sb strings.Builder
	if err := toml.NewEncoder(&sb).Encode(c); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}
