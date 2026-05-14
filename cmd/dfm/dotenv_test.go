package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a tiny helper for the dotenv tests.
func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

// writeConfig writes a minimal config.toml with the given runtime.dotenv value.
func writeConfig(t *testing.T, dir, dotenv string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	body := "[runtime]\ndotenv = \"" + dotenv + "\"\n"
	writeFile(t, path, body, 0o600)
	return path
}

// resetDotenvSource sets dotenvSource back to "none" after a test so
// cross-test bleed doesn't muddy assertions.
func resetDotenvSource(t *testing.T) {
	t.Helper()
	prev := dotenvSource
	t.Cleanup(func() { dotenvSource = prev })
	dotenvSource = "none"
}

func TestDotenv_ConfigAutoLoadsEnvFile(t *testing.T) {
	resetDotenvSource(t)
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	envPath := filepath.Join(xdg, "dotfiles", ".env")
	writeFile(t, envPath, "TURSO_DATABASE_URL=libsql://test\n", 0o600)
	cfgPath := writeConfig(t, dir, "auto")

	t.Setenv("XDG_CONFIG_HOME", xdg)
	_ = os.Unsetenv("TURSO_DATABASE_URL")
	t.Cleanup(func() { _ = os.Unsetenv("TURSO_DATABASE_URL") })

	src, err := resolveAndLoadDotenv(false, "", "", cfgPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if src != envPath {
		t.Errorf("source = %q, want %q", src, envPath)
	}
	if got := os.Getenv("TURSO_DATABASE_URL"); got != "libsql://test" {
		t.Errorf("TURSO_DATABASE_URL = %q, want libsql://test", got)
	}
}

func TestDotenv_ShellEnvWinsOverFile(t *testing.T) {
	resetDotenvSource(t)
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	envPath := filepath.Join(xdg, "dotfiles", ".env")
	writeFile(t, envPath, "TURSO_DATABASE_URL=libsql://fromfile\n", 0o600)
	cfgPath := writeConfig(t, dir, "auto")

	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("TURSO_DATABASE_URL", "libsql://fromshell")

	if _, err := resolveAndLoadDotenv(false, "", "", cfgPath); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := os.Getenv("TURSO_DATABASE_URL"); got != "libsql://fromshell" {
		t.Errorf("shell value clobbered: got %q, want libsql://fromshell", got)
	}
}

func TestDotenv_EmptyConfigIgnoresFile(t *testing.T) {
	resetDotenvSource(t)
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	envPath := filepath.Join(xdg, "dotfiles", ".env")
	writeFile(t, envPath, "DFM_DOTENV_IGNORED=should-not-set\n", 0o600)
	cfgPath := writeConfig(t, dir, "")

	t.Setenv("XDG_CONFIG_HOME", xdg)
	_ = os.Unsetenv("DFM_DOTENV_IGNORED")
	t.Cleanup(func() { _ = os.Unsetenv("DFM_DOTENV_IGNORED") })

	src, err := resolveAndLoadDotenv(false, "", "", cfgPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if src != "none" {
		t.Errorf("source = %q, want none", src)
	}
	if v := os.Getenv("DFM_DOTENV_IGNORED"); v != "" {
		t.Errorf("env var leaked from disabled dotenv: %q", v)
	}
}

func TestDotenv_ExplicitMissingPathErrors(t *testing.T) {
	resetDotenvSource(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, filepath.Join(dir, "nope.env"))

	_, err := resolveAndLoadDotenv(false, "", "", cfgPath)
	if err == nil {
		t.Fatal("expected error for missing dotenv file")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %T", err)
	}
	if ee.code != exitResolveErr {
		t.Errorf("exit code = %d, want %d", ee.code, exitResolveErr)
	}
}

func TestDotenv_EnvVarBeatsConfig(t *testing.T) {
	resetDotenvSource(t)
	dir := t.TempDir()

	// Config points at file A (would set FOO=fromconfig).
	cfgEnvPath := filepath.Join(dir, "config.env")
	writeFile(t, cfgEnvPath, "DFM_DOTENV_TEST=fromconfig\n", 0o600)
	cfgPath := writeConfig(t, dir, cfgEnvPath)

	// DFM_ENV_FILE points at file B (would set FOO=fromenvvar).
	envvarPath := filepath.Join(dir, "envvar.env")
	writeFile(t, envvarPath, "DFM_DOTENV_TEST=fromenvvar\n", 0o600)

	_ = os.Unsetenv("DFM_DOTENV_TEST")
	t.Cleanup(func() { _ = os.Unsetenv("DFM_DOTENV_TEST") })

	src, err := resolveAndLoadDotenv(false, "", envvarPath, cfgPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if src != envvarPath {
		t.Errorf("source = %q, want envvar path %q", src, envvarPath)
	}
	if got := os.Getenv("DFM_DOTENV_TEST"); got != "fromenvvar" {
		t.Errorf("DFM_DOTENV_TEST = %q, want fromenvvar", got)
	}
}

func TestDotenv_LoosePermsWarnsButLoads(t *testing.T) {
	resetDotenvSource(t)
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	envPath := filepath.Join(xdg, "dotfiles", ".env")

	cases := []struct {
		name      string
		mode      os.FileMode
		wantWarn  bool
	}{
		{"perm_0644_warns", 0o644, true},
		{"perm_0600_silent", 0o600, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, envPath, "DFM_DOTENV_PERM=ok\n", tc.mode)
			// chmod again — WriteFile honors umask on existing files.
			if err := os.Chmod(envPath, tc.mode); err != nil {
				t.Fatal(err)
			}
			cfgPath := writeConfig(t, dir, "auto")
			t.Setenv("XDG_CONFIG_HOME", xdg)
			_ = os.Unsetenv("DFM_DOTENV_PERM")

			// Redirect stderr to capture the warning.
			oldStderr := os.Stderr
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = w
			done := make(chan string)
			go func() {
				var buf bytes.Buffer
				_, _ = io.Copy(&buf, r)
				done <- buf.String()
			}()

			_, derr := resolveAndLoadDotenv(false, "", "", cfgPath)

			_ = w.Close()
			os.Stderr = oldStderr
			stderr := <-done

			if derr != nil {
				t.Fatalf("resolve: %v", derr)
			}
			gotWarn := strings.Contains(stderr, "loose permissions")
			if gotWarn != tc.wantWarn {
				t.Errorf("warn=%v stderr=%q wantWarn=%v", gotWarn, stderr, tc.wantWarn)
			}
		})
	}
}
