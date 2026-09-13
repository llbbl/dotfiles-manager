package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

const testAuthToken = "ey-super-secret-token-value"

func runConfigShow(t *testing.T, cfg *config.Config, args ...string) (string, error) {
	t.Helper()
	cmd := newConfigCmd()
	cmd.SetContext(config.WithContext(context.Background(), cfg))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(append([]string{"show"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func showConfig() *config.Config {
	return &config.Config{
		AI:  config.AIConfig{Provider: "claude-code"},
		Log: config.LogConfig{Backend: "both"},
		State: config.StateConfig{
			URL:       "libsql://db-org.turso.io/path?authToken=" + testAuthToken,
			AuthToken: testAuthToken,
		},
	}
}

func TestConfigShow_Default_RedactsTokenAndURL(t *testing.T) {
	out, err := runConfigShow(t, showConfig())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out, testAuthToken) {
		t.Fatalf("token leaked into output:\n%s", out)
	}
	if !strings.Contains(out, `auth_token = "<redacted>"`) {
		t.Errorf("want redaction placeholder, got:\n%s", out)
	}
	if !strings.Contains(out, `url = "libsql://db-org.turso.io"`) {
		t.Errorf("want scrubbed url, got:\n%s", out)
	}
	if !strings.Contains(out, "# dotenv_source =") {
		t.Errorf("want dotenv_source header, got:\n%s", out)
	}
}

func TestConfigShow_UnsetToken_OmitsKey(t *testing.T) {
	cfg := showConfig()
	cfg.State.AuthToken = ""
	out, err := runConfigShow(t, cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out, "auth_token") {
		t.Errorf("auth_token key should be absent when unset, got:\n%s", out)
	}
}

func TestConfigShow_FileURLSurvivesScrub(t *testing.T) {
	cfg := showConfig()
	cfg.State.URL = "file:///home/u/.local/share/dotfiles/state.db"
	out, err := runConfigShow(t, cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, cfg.State.URL) {
		t.Errorf("file:// url should print in full, got:\n%s", out)
	}
}

func TestConfigShow_WhitespaceURL_StillScrubbed(t *testing.T) {
	cfg := showConfig()
	cfg.State.URL = " " + cfg.State.URL + "\n"
	out, err := runConfigShow(t, cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out, testAuthToken) {
		t.Fatalf("token leaked through a padded url:\n%s", out)
	}
}

// The --show-secrets success branch needs a terminal, which a test
// cannot hand cobra, so the body is exercised directly instead.
func TestConfigShowBody_ShowSecrets(t *testing.T) {
	b, err := configShowBody(showConfig(), true)
	if err != nil {
		t.Fatalf("configShowBody: %v", err)
	}
	if !strings.Contains(string(b), testAuthToken) {
		t.Errorf("--show-secrets should print the real token, got:\n%s", b)
	}
	redacted, err := configShowBody(showConfig(), false)
	if err != nil {
		t.Fatalf("configShowBody: %v", err)
	}
	if strings.Contains(string(redacted), testAuthToken) {
		t.Errorf("default should redact, got:\n%s", redacted)
	}
}

func TestConfigShow_ShowSecretsNonTTY_Refuses(t *testing.T) {
	out, err := runConfigShow(t, showConfig(), "--show-secrets")
	if err == nil {
		t.Fatalf("expected refusal, got output:\n%s", out)
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %T: %v", err, err)
	}
	if ee.code != exitInitNoTTY {
		t.Errorf("exit = %d, want %d", ee.code, exitInitNoTTY)
	}
	if strings.Contains(out, testAuthToken) {
		t.Fatalf("token leaked on refusal path:\n%s", out)
	}
}
