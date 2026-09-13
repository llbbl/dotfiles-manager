package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func TestLibsqlURLHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"libsql://my-db-user.turso.io", "my-db-user.turso.io"},
		{"libsql://host.example.com/some/path?token=secret", "host.example.com"},
		{"", ""},
		{"::::not-a-url", ""},
	}
	for _, tc := range cases {
		got := libsqlURLHost(tc.in)
		if got != tc.want {
			t.Errorf("libsqlURLHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractTursoToken(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "eyJabc.def.ghi\n", "eyJabc.def.ghi"},
		{"no-trailing-newline", "tok123", "tok123"},
		{"header-then-token", "Token created:\n\neyJabc.def.ghi\n", "eyJabc.def.ghi"},
		{"trailing-blank-lines", "tok\n\n\n", "tok"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTursoToken(tc.in)
			if got != tc.want {
				t.Errorf("extractTursoToken(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestWriteTursoEnvFile_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := writeTursoEnvFile(path, "tok123"); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "TURSO_AUTH_TOKEN=tok123\n" {
		t.Errorf("content = %q", got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

func TestWriteTursoEnvFile_PreservesOtherVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	initial := "FOO=bar\nBAZ=qux\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeTursoEnvFile(path, "tok123"); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "FOO=bar") || !strings.Contains(got, "BAZ=qux") {
		t.Errorf("other vars lost: %q", got)
	}
	if !strings.Contains(got, "TURSO_AUTH_TOKEN=tok123") {
		t.Errorf("token missing: %q", got)
	}
}

func TestWriteTursoEnvFile_ReplacesExistingToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	initial := "FOO=bar\nTURSO_AUTH_TOKEN=oldvalue\nBAZ=qux\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeTursoEnvFile(path, "newtok"); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "oldvalue") {
		t.Errorf("old token not replaced: %q", got)
	}
	if !strings.Contains(got, "TURSO_AUTH_TOKEN=newtok") {
		t.Errorf("new token missing: %q", got)
	}
	// Ensure no duplicate TURSO_AUTH_TOKEN= lines.
	count := strings.Count(got, "TURSO_AUTH_TOKEN=")
	if count != 1 {
		t.Errorf("TURSO_AUTH_TOKEN= appears %d times, want 1: %q", count, got)
	}
	// Mode must remain 0600.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

func TestWriteTursoEnvFile_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "subdir", ".env")
	if err := writeTursoEnvFile(path, "tok"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

// config.Load overlays TURSO_AUTH_TOKEN, so saving the struct back could
// persist an env-only token. Blanking it unconditionally is equally
// wrong: omitempty would then delete a token the file legitimately owns.
func TestSaveConfigKeepingFileToken(t *testing.T) {
	for _, tc := range []struct {
		name, fileToken, envToken, want string
	}{
		{"env only", "", "env-secret", ""},
		{"file only", "file-secret", "", "file-secret"},
		{"both", "file-secret", "env-secret", "file-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")

			seed := config.Defaults()
			seed.State.URL = "file://" + filepath.Join(dir, "state.db")
			if tc.fileToken != "" {
				seed.State.AuthToken = tc.fileToken
			}
			if err := config.Save(cfgPath, seed); err != nil {
				t.Fatalf("seed config: %v", err)
			}

			t.Setenv("TURSO_AUTH_TOKEN", tc.envToken)
			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if tc.envToken != "" && cfg.State.AuthToken != tc.envToken {
				t.Fatalf("Load did not overlay env token: got %q", cfg.State.AuthToken)
			}

			cfg.State.URL = "libsql://new-db.turso.io"
			if err := saveConfigKeepingFileToken(cfg, cfgPath); err != nil {
				t.Fatalf("saveConfigKeepingFileToken: %v", err)
			}

			got, err := config.SavedAuthToken(cfgPath)
			if err != nil {
				t.Fatalf("SavedAuthToken: %v", err)
			}
			if got != tc.want {
				t.Errorf("auth_token in config.toml = %q, want %q", got, tc.want)
			}

			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)
			if !strings.Contains(body, "libsql://new-db.turso.io") {
				t.Errorf("URL not persisted: %s", body)
			}
			if tc.envToken != "" && strings.Contains(body, tc.envToken) {
				t.Errorf("env-only token leaked into config.toml: %s", body)
			}
			if tc.want == "" && strings.Contains(body, "auth_token") {
				t.Errorf("absent token should stay omitted, got: %s", body)
			}
		})
	}
}
