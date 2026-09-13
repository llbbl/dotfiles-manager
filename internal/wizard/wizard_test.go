package wizard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

// helper: run the wizard with the given stdin string + options and
// return the plan, captured stdout, and any error. HOME and both XDG
// roots are pinned to a temp dir so no test can reach the developer's
// real config.toml or .env.
func runWizard(t *testing.T, input string, opts Options, existing *config.Config) (*Plan, string, error) {
	t.Helper()
	pinHome(t)
	var out bytes.Buffer
	opts.In = strings.NewReader(input)
	opts.Out = &out
	plan, err := Run(opts, existing)
	return plan, out.String(), err
}

func pinHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
}

// envFilePath is where the wizard will put credentials under the pinned
// HOME. Only valid after runWizard has pinned it.
func envFilePath(t *testing.T) string {
	t.Helper()
	p := config.EnvFilePath()
	return p
}

// TestWizard_FreshDefaults_WritesExpectedTOML locks the "fresh setup,
// accept everything" acceptance criterion: an all-empty input stream
// yields the documented defaults and the resulting config.toml is
// mode 0600.
func TestWizard_FreshDefaults_WritesExpectedTOML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// Six chapters worth of empty lines: config path, claude bin,
	// model, state branch, repo URL, track-nudge.
	in := strings.Repeat("\n", 8)

	plan, _, err := runWizard(t, in, Options{ConfigPath: cfgPath}, nil)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	if !plan.ConfigWritten {
		t.Fatal("expected ConfigWritten=true")
	}

	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.AI.Provider != "claude-code" {
		t.Errorf("ai.provider = %q, want claude-code", got.AI.Provider)
	}
	if got.AI.ClaudeCode.Model != "sonnet" {
		t.Errorf("ai.claude-code.model = %q, want sonnet", got.AI.ClaudeCode.Model)
	}
	if !strings.HasPrefix(got.State.URL, "file://") {
		t.Errorf("state.url = %q, want file://...", got.State.URL)
	}
	if got.Repo.Remote != "" {
		t.Errorf("repo.remote = %q, want empty", got.Repo.Remote)
	}
	if plan.RepoFlow {
		t.Error("RepoFlow=true on empty remote")
	}
	if plan.ProvisionTurso {
		t.Error("ProvisionTurso=true on local-default flow")
	}
}

// TestWizard_TursoFromEnv_BakesValues locks: env vars present + user
// picks turso + accepts the env-bake offer => the URL lands in config,
// the token lands in the env file, ProvisionTurso is false (no CLI
// shell-out).
func TestWizard_TursoFromEnv_BakesValues(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	t.Setenv("TURSO_DATABASE_URL", "libsql://test-host.turso.io")
	t.Setenv("TURSO_AUTH_TOKEN", "env-secret")

	// Inputs: config path \n, ai bin \n, ai model \n, state="b"\n,
	// env-bake "y"\n, repo "" \n, track "" \n.
	in := "\n\n\nb\ny\n\n\n"

	plan, out, err := runWizard(t, in, Options{ConfigPath: cfgPath}, nil)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	if plan.ProvisionTurso {
		t.Error("ProvisionTurso should be false when env vars are baked")
	}
	if !strings.Contains(out, "the auth token goes to the env file") {
		t.Error("missing env-file notice")
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.URL != "libsql://test-host.turso.io" {
		t.Errorf("state.url = %q", got.State.URL)
	}
	// config.Load applies the TURSO_AUTH_TOKEN env override, so we
	// can't assert the on-disk file via Load. Inspect the raw bytes.
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "env-secret") || strings.Contains(string(raw), "auth_token") {
		t.Error("auth token leaked into config.toml")
	}
	env, err := os.ReadFile(envFilePath(t))
	if err != nil {
		t.Fatalf("env file: %v", err)
	}
	if !strings.Contains(string(env), "TURSO_AUTH_TOKEN=env-secret") {
		t.Error("baked token not written to the env file")
	}
}

// TestWizard_TursoNoEnv_PromptsForBoth locks: empty env + user picks
// turso => wizard prompts for both URL and token interactively.
func TestWizard_TursoNoEnv_PromptsForBoth(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "")

	// Inputs: cfg path, ai bin, ai model, state=b, url, token, repo, track.
	in := "\n\n\nb\nlibsql://manual.turso.io\nprompt-secret\n\n\n"

	_, _, err := runWizard(t, in, Options{ConfigPath: cfgPath}, nil)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	raw, _ := os.ReadFile(cfgPath)
	got := string(raw)
	if !strings.Contains(got, "libsql://manual.turso.io") {
		t.Error("URL not persisted to config.toml")
	}
	if strings.Contains(got, "prompt-secret") || strings.Contains(got, "auth_token") {
		t.Error("prompted token leaked into config.toml")
	}
	env, err := os.ReadFile(envFilePath(t))
	if err != nil {
		t.Fatalf("env file: %v", err)
	}
	if !strings.Contains(string(env), "TURSO_AUTH_TOKEN=prompt-secret") {
		t.Error("prompted token not written to the env file")
	}
}

// Regression: TURSO_AUTH_TOKEN set, TURSO_DATABASE_URL unset, no turso
// CLI. Load overlaid the env token onto the config the wizard cloned in
// edit mode, so re-running init wrote a credential the file never held
// into config.toml — before the provisioning step's guard could run.
func TestWizard_EditMode_EnvTokenNeverReachesConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	seed := config.Defaults()
	seed.State.URL = "libsql://existing.turso.io"
	if err := config.Save(cfgPath, seed); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "env-secret")

	existing, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if existing.State.AuthToken != "env-secret" {
		t.Fatalf("setup: Load did not overlay the env token")
	}

	plan, _, err := runWizard(t, strings.Repeat("\n", 8), Options{ConfigPath: cfgPath, Turso: true}, existing)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	if !plan.ProvisionTurso {
		t.Error("expected the CLI provisioning branch with no URL in the env")
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, "env-secret") {
		t.Error("env-only token written to config.toml")
	}
	if strings.Contains(body, "auth_token") {
		t.Error("config.toml carries an auth_token key")
	}
	if tok, err := config.SavedAuthToken(cfgPath); err != nil || tok != "" {
		t.Errorf("SavedAuthToken = %v (err=%v), want empty", tok != "", err)
	}
}

// TestWizard_EditMode_PreFillsCurrentValues locks: re-running on an
// existing config without --force uses each current value as the Enter
// default. We verify the model chapter: existing model="opus", accept
// default, post-config still has model="opus".
func TestWizard_EditMode_PreFillsCurrentValues(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	existing := config.Defaults()
	existing.AI.ClaudeCode.Model = "opus"
	existing.AI.ClaudeCode.Bin = "/custom/claude"
	if err := config.Save(cfgPath, existing); err != nil {
		t.Fatal(err)
	}

	// All empty lines => accept every pre-fill.
	in := strings.Repeat("\n", 8)
	_, _, err := runWizard(t, in, Options{ConfigPath: cfgPath}, existing)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.AI.ClaudeCode.Model != "opus" {
		t.Errorf("model = %q, want opus", got.AI.ClaudeCode.Model)
	}
	if got.AI.ClaudeCode.Bin != "/custom/claude" {
		t.Errorf("bin = %q, want /custom/claude", got.AI.ClaudeCode.Bin)
	}
}

// TestWizard_YesFreshConfig locks the --yes shortcut: no prompts, all
// defaults, exits successfully with a summary.
func TestWizard_YesFreshConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	plan, out, err := runWizard(t, "", Options{ConfigPath: cfgPath, Yes: true}, nil)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	if !plan.ConfigWritten {
		t.Fatal("--yes should write the config")
	}
	if !strings.Contains(out, "wrote config:") {
		t.Errorf("summary missing. out=%q", out)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.AI.ClaudeCode.Model != "sonnet" {
		t.Errorf("--yes did not pick sonnet default: %q", got.AI.ClaudeCode.Model)
	}
}

// TestWizard_Print_NoFileWritten locks --print: TOML emitted to stdout,
// nothing on disk at the config path.
func TestWizard_Print_NoFileWritten(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	plan, out, err := runWizard(t, "", Options{ConfigPath: cfgPath, Yes: true, Print: true}, nil)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	if plan.ConfigWritten {
		t.Error("--print must not write the config")
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("file should not exist at %s (err=%v)", cfgPath, err)
	}
	if !strings.Contains(out, "[ai.claude-code]") {
		t.Errorf("stdout should contain rendered TOML. out=%q", out)
	}
	if !strings.Contains(out, "would write config:") {
		t.Errorf("summary should say 'would write' in --print mode. out=%q", out)
	}
}

// TestWizard_ForceOnExistingConfig locks --force: overwrites the file
// using fresh defaults (no edit-mode pre-fills). Even though existing
// has model=opus, an all-empty input ends up with the default sonnet.
func TestWizard_ForceOnExistingConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	existing := config.Defaults()
	existing.AI.ClaudeCode.Model = "opus"
	if err := config.Save(cfgPath, existing); err != nil {
		t.Fatal(err)
	}

	in := strings.Repeat("\n", 8)
	_, _, err := runWizard(t, in, Options{ConfigPath: cfgPath, Force: true}, existing)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.AI.ClaudeCode.Model != "sonnet" {
		t.Errorf("--force should reset to defaults, got model=%q", got.AI.ClaudeCode.Model)
	}
}

// --print previews the file that would be written, and that file never
// holds auth_token. Emitting it would print a credential and show a
// preview the write path contradicts.
func TestWizard_Print_OmitsAuthToken(t *testing.T) {
	t.Setenv("TURSO_DATABASE_URL", "libsql://fake.turso.io")
	t.Setenv("TURSO_AUTH_TOKEN", "env-secret")

	_, out, err := runWizard(t, "", Options{Yes: true, Turso: true, Print: true}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if strings.Contains(out, "env-secret") {
		t.Error("--print emitted the auth token")
	}
	if strings.Contains(out, "auth_token") {
		t.Errorf("--print rendered an auth_token key:\n%s", out)
	}
}
