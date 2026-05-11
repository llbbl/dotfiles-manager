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
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	orig := Defaults()
	orig.Repo.Remote = "git@github.com:llbbl/dotfiles-private.git"
	orig.AI.ClaudeCode.Model = "sonnet"
	orig.AI.ClaudeCode.ExtraArgs = []string{"--foo", "--bar"}

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

func TestTursoAuthTokenEnvOverride(t *testing.T) {
	t.Setenv("TURSO_AUTH_TOKEN", "secret-token-xyz")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State.AuthToken != "secret-token-xyz" {
		t.Errorf("expected env override, got %q", cfg.State.AuthToken)
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
