// Package config loads, validates, and persists the TOML configuration
// that drives every other subsystem. Load merges Defaults with the file
// at the given path, expands "~/" prefixes, and overlays a small set of
// environment variables (TURSO_*, DFM_LOG_BACKEND). The resulting
// *Config can be attached to a context via WithContext / FromContext.
package config

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/llbbl/dotfiles-manager/internal/envfile"
	"github.com/llbbl/dotfiles-manager/internal/fsx"
)

// Config is the fully-resolved TOML configuration used throughout the
// application.
type Config struct {
	Repo    RepoConfig    `toml:"repo"`
	AI      AIConfig      `toml:"ai"`
	Log     LogConfig     `toml:"log"`
	State   StateConfig   `toml:"state"`
	Backup  BackupConfig  `toml:"backup"`
	Runtime RuntimeConfig `toml:"runtime"`
	Path    PathConfig    `toml:"path"`
}

// PathConfig records where dfm writes managed PATH entries. UseFragment
// is off until `dfm path migrate` turns it on; once on, the generated
// fragment is the source of truth and the shell's rc file is no longer
// read or written for PATH.
type PathConfig struct {
	UseFragment bool `toml:"use_fragment"`
}

// RuntimeConfig groups settings that affect how the dfm process starts
// up, before the main config is read. The only field today is Dotenv,
// which controls .env-file injection of process environment variables.
//
// Dotenv values:
//
//	""        — disabled (default)
//	"off"     — disabled (explicit)
//	"auto"    — load $XDG_CONFIG_HOME/dotfiles/.env or ./.env if present
//	"<path>"  — explicit path; missing file is an error
type RuntimeConfig struct {
	Dotenv string `toml:"dotenv"`
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
// DFM_LOG_BACKEND environment overrides. A missing file is not an
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
	if v := os.Getenv("DFM_LOG_BACKEND"); v != "" {
		cfg.Log.Backend = strings.ToLower(v)
	}
	if cfg.Log.Backend == "" {
		cfg.Log.Backend = "both"
	}

	return cfg, nil
}

// LoadRuntimeOnly parses just the [runtime] section from path. It is
// intended for the early-startup dotenv resolver, which must read this
// section BEFORE the full config Load (because dotenv-injected vars
// like TURSO_DATABASE_URL participate in the real Load).
//
// A missing or unreadable file yields a zero RuntimeConfig and nil
// error — the caller decides whether that's acceptable.
func LoadRuntimeOnly(path string) (RuntimeConfig, error) {
	if path == "" {
		return RuntimeConfig{}, nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimeConfig{}, nil
		}
		return RuntimeConfig{}, fmt.Errorf("stat %s: %w", path, err)
	}
	var shell struct {
		Runtime RuntimeConfig `toml:"runtime"`
	}
	if _, err := toml.DecodeFile(path, &shell); err != nil {
		return RuntimeConfig{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return shell.Runtime, nil
}

// Save writes cfg as TOML to path, creating parent directories as
// needed. The file is always 0600: it has historically carried
// state.auth_token, and an explicit mode narrows an existing
// world-readable file instead of preserving its bits.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := cfg.EncodeTOML()
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return fsx.AtomicWrite(path, data, 0o600)
}

// EnvFilePath returns $XDG_DATA_HOME/dotfiles/.env — the 0600 file that
// holds credentials, alongside state.db and the backups.
func EnvFilePath() string {
	return filepath.Join(xdgDataHome(), "dotfiles", ".env")
}

// TokenMigrationNotice is the message a caller prints after
// SaveKeepingFileToken reports a migration. It has to name the shell
// step: nothing in dfm reads the env file, so a token that worked from
// config.toml stops working until the user's shell exports it.
func TokenMigrationNotice(envPath string) string {
	return fmt.Sprintf(`→ moved state.auth_token from config.toml
  to %s (0600)
  config.toml no longer holds a credential

  Next step: ensure your shell loads %s, or export TURSO_AUTH_TOKEN
  in your environment. dfm reads $TURSO_AUTH_TOKEN at runtime, not this
  file — until you do, remote state will not authenticate.
`, envPath, envPath)
}

// SaveKeepingFileToken writes cfg to path with state.auth_token always
// omitted, and is the only way a token-bearing Config should reach disk.
// Load overlays TURSO_AUTH_TOKEN from the environment, so the struct in
// hand routinely carries a token the file never held.
//
// A token the file itself owns is not dropped — it is moved to the env
// file and migrated=true is returned so the caller can say so. The env
// file is written first: a failure between the two writes must not
// destroy the only copy of the credential.
func SaveKeepingFileToken(path string, cfg *Config) (migrated bool, envPath string, err error) {
	fileToken, err := SavedAuthToken(path)
	if err != nil {
		return false, "", err
	}
	envPath = EnvFilePath()
	if fileToken != "" {
		if err := envfile.SetKey(envPath, "TURSO_AUTH_TOKEN", fileToken); err != nil {
			return false, envPath, err
		}
	}

	saved := *cfg
	saved.State.AuthToken = ""
	if err := Save(path, &saved); err != nil {
		return false, envPath, fmt.Errorf("save config %s: %w", path, err)
	}
	return fileToken != "", envPath, nil
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

// Redacted returns a display-only copy: the auth token becomes a fixed
// placeholder and the state URL is cut back to scheme://host. An absent
// token stays absent, so a hidden token reads differently from none at
// all. Nothing on the write path goes through here.
func (c *Config) Redacted() *Config {
	out := *c
	if out.State.AuthToken != "" {
		out.State.AuthToken = "<redacted>"
	}
	// store trims the URL before using it, so a value with stray
	// whitespace still works at runtime — and would parse to an empty
	// Host here and pass through with its query string intact.
	out.State.URL = scrubURL(strings.TrimSpace(out.State.URL))
	return &out
}

// scrubURL keeps a URL to scheme://host. An unparseable or host-less URL
// passes through unchanged so file:///... paths stay readable. Duplicated
// from internal/store rather than exported: store imports this package.
func scrubURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return u
	}
	return parsed.Scheme + "://" + parsed.Host
}

// SavedAuthToken returns the state.auth_token already written in the
// config file, or "" when the file has none. Load overlays
// TURSO_AUTH_TOKEN from the environment, so a Config in hand can carry a
// token the file never held; write this back instead to keep an env-only
// token out of the file without deleting one the file owns. A missing
// file is not an error.
func SavedAuthToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	var onDisk Config
	if _, err := toml.DecodeFile(path, &onDisk); err != nil {
		return "", fmt.Errorf("decode %s: %w", path, err)
	}
	return onDisk.State.AuthToken, nil
}

// FragmentPath returns the canonical location of a generated shell
// fragment (e.g. "env.sh") under $XDG_CONFIG_HOME/dotfiles/, alongside
// config.toml — one config directory per tool.
func FragmentPath(name string) string {
	return filepath.Join(xdgConfigHome(), "dotfiles", name)
}
