package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.toml")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AI.Provider != "claude-code" {
		t.Errorf("expected default provider claude-code, got %q", cfg.AI.Provider)
	}
	if cfg.AI.ClaudeCode.Bin != "claude" {
		t.Errorf("expected default bin claude, got %q", cfg.AI.ClaudeCode.Bin)
	}
	if cfg.Repo.Local == "" || strings.Contains(cfg.Repo.Local, "~") {
		t.Errorf("repo.local not expanded: %q", cfg.Repo.Local)
	}
	if cfg.Log.Backend != "both" {
		t.Errorf("log.backend default = %q, want both", cfg.Log.Backend)
	}
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	orig := Defaults()
	orig.Repo.Remote = "git@github.com:llbbl/dotfiles-private.git"
	orig.AI.ClaudeCode.Model = "sonnet"
	orig.AI.ClaudeCode.ExtraArgs = []string{"--foo", "--bar"}
	orig.Log.Backend = "jsonl"

	if err := Save(path, orig); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Repo.Remote != orig.Repo.Remote {
		t.Errorf("remote mismatch: got %q want %q", loaded.Repo.Remote, orig.Repo.Remote)
	}
	if loaded.AI.ClaudeCode.Model != "sonnet" {
		t.Errorf("model mismatch: %q", loaded.AI.ClaudeCode.Model)
	}
	if len(loaded.AI.ClaudeCode.ExtraArgs) != 2 {
		t.Errorf("extra_args mismatch: %#v", loaded.AI.ClaudeCode.ExtraArgs)
	}
	if loaded.Log.Backend != "jsonl" {
		t.Errorf("log.backend mismatch: %q", loaded.Log.Backend)
	}
}

func TestLogBackendEnvOverride(t *testing.T) {
	t.Setenv("DFM_LOG_BACKEND", "DB")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Log.Backend != "db" {
		t.Errorf("expected env override to lowercase 'db', got %q", cfg.Log.Backend)
	}

	t.Setenv("DFM_LOG_BACKEND", "")
	cfg, err = Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Log.Backend != "both" {
		t.Errorf("expected empty env to fall back to default 'both', got %q", cfg.Log.Backend)
	}
}

func TestValidate_RejectsUnknownLogBackend(t *testing.T) {
	cfg := Defaults()
	cfg.Log.Backend = "xyz"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for unknown log.backend")
	}
	if !strings.Contains(err.Error(), "unknown log.backend") {
		t.Errorf("error missing expected substring: %v", err)
	}
}

func TestHomeExpansion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[repo]
local = "~/foo/repo"

[ai]
provider = "claude-code"

[ai.claude-code]
bin = "claude"

[log]
path = "~/foo/log.jsonl"

[state]
url = "file:///~/foo/state.db"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if cfg.Repo.Local != filepath.Join(home, "foo", "repo") {
		t.Errorf("repo.local not expanded: %q", cfg.Repo.Local)
	}
	if cfg.Log.Path != filepath.Join(home, "foo", "log.jsonl") {
		t.Errorf("log.path not expanded: %q", cfg.Log.Path)
	}
	if !strings.HasPrefix(cfg.State.URL, "file://"+home) {
		t.Errorf("state.url not expanded: %q", cfg.State.URL)
	}
}

func TestTursoEnvOverrides(t *testing.T) {
	t.Setenv("TURSO_AUTH_TOKEN", "secret-token-xyz")
	t.Setenv("TURSO_DATABASE_URL", "libsql://example-org.turso.io")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State.AuthToken != "secret-token-xyz" {
		t.Errorf("expected token env override, got %q", cfg.State.AuthToken)
	}
	if cfg.State.URL != "libsql://example-org.turso.io" {
		t.Errorf("expected url env override, got %q", cfg.State.URL)
	}
}

func TestTursoDatabaseURLEnvOverride(t *testing.T) {
	t.Setenv("TURSO_DATABASE_URL", "libsql://only-url.turso.io")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State.URL != "libsql://only-url.turso.io" {
		t.Errorf("expected env override, got %q", cfg.State.URL)
	}
}

func TestBackupConfig_DefaultsAndRoundtrip(t *testing.T) {
	cfg := Defaults()
	if cfg.Backup.MaxTotalMB != 500 {
		t.Errorf("MaxTotalMB default = %d, want 500", cfg.Backup.MaxTotalMB)
	}
	if cfg.Backup.RetentionDays != 90 {
		t.Errorf("RetentionDays default = %d, want 90", cfg.Backup.RetentionDays)
	}
	if cfg.Backup.Dir == "" || strings.Contains(cfg.Backup.Dir, "~") {
		t.Errorf("backup.dir not expanded: %q", cfg.Backup.Dir)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg.Backup.Dir = "/var/tmp/dfm-backups"
	cfg.Backup.MaxTotalMB = 1024
	cfg.Backup.RetentionDays = 30
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Backup.Dir != "/var/tmp/dfm-backups" {
		t.Errorf("backup.dir mismatch: %q", loaded.Backup.Dir)
	}
	if loaded.Backup.MaxTotalMB != 1024 {
		t.Errorf("MaxTotalMB mismatch: %d", loaded.Backup.MaxTotalMB)
	}
	if loaded.Backup.RetentionDays != 30 {
		t.Errorf("RetentionDays mismatch: %d", loaded.Backup.RetentionDays)
	}
}

func TestValidate_RejectsUnknownProvider(t *testing.T) {
	cfg := Defaults()
	cfg.AI.Provider = "bogus"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for unknown provider")
	}

	cfg.AI.Provider = "claude-code"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// An existing config.toml predates the [path] section, so its absence
// has to leave the switch off rather than needing a config migration.
func TestLoad_AbsentPathSectionLeavesUseFragmentOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[log]\nbackend = \"jsonl\"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path.UseFragment {
		t.Error("path.use_fragment defaulted to true")
	}

	cfg.Path.UseFragment = true
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !back.Path.UseFragment {
		t.Error("path.use_fragment did not round-trip")
	}
}

func TestSavedAuthToken(t *testing.T) {
	dir := t.TempDir()

	if got, err := SavedAuthToken(""); err != nil || got != "" {
		t.Errorf("empty path = (%q, %v), want (\"\", nil)", got, err)
	}

	missing := filepath.Join(dir, "absent.toml")
	if got, err := SavedAuthToken(missing); err != nil || got != "" {
		t.Errorf("missing file = (%q, %v), want (\"\", nil)", got, err)
	}

	path := filepath.Join(dir, "config.toml")
	cfg := Defaults()
	cfg.State.AuthToken = "file-secret"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The env overlay is exactly what this helper exists to see past.
	t.Setenv("TURSO_AUTH_TOKEN", "env-secret")
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.State.AuthToken != "env-secret" {
		t.Fatalf("Load did not overlay the env token; got %q", loaded.State.AuthToken)
	}
	got, err := SavedAuthToken(path)
	if err != nil {
		t.Fatalf("SavedAuthToken: %v", err)
	}
	if got != "file-secret" {
		t.Errorf("SavedAuthToken = %q, want the file's own token", got)
	}

	if err := os.WriteFile(path, []byte("this is not toml = = ="), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := SavedAuthToken(path); err == nil {
		t.Error("malformed config did not error")
	}
}

func TestRedacted_LeavesReceiverIntact(t *testing.T) {
	cfg := Defaults()
	cfg.State.URL = "libsql://db-org.turso.io/v2?authToken=real-token"
	cfg.State.AuthToken = "real-token"

	red := cfg.Redacted()
	if red.State.AuthToken != "<redacted>" {
		t.Errorf("redacted token = %q", red.State.AuthToken)
	}
	if red.State.URL != "libsql://db-org.turso.io" {
		t.Errorf("redacted url = %q", red.State.URL)
	}
	if cfg.State.AuthToken != "real-token" || !strings.Contains(cfg.State.URL, "authToken=") {
		t.Fatalf("Redacted mutated its receiver: %q / %q", cfg.State.AuthToken, cfg.State.URL)
	}
}

func TestRedacted_PaddedURLStillScrubbed(t *testing.T) {
	cfg := Defaults()
	cfg.State.URL = "  libsql://db-org.turso.io/v2?authToken=real-token\n"
	if got := cfg.Redacted().State.URL; got != "libsql://db-org.turso.io" {
		t.Errorf("padded url = %q, want it scrubbed", got)
	}
}

func TestRedacted_EmptyTokenStaysAbsent(t *testing.T) {
	cfg := Defaults()
	b, err := cfg.Redacted().EncodeTOML()
	if err != nil {
		t.Fatalf("EncodeTOML: %v", err)
	}
	if strings.Contains(string(b), "auth_token") {
		t.Errorf("auth_token key should be omitted when unset:\n%s", b)
	}
}
