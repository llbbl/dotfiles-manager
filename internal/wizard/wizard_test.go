package wizard

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func TestWizard_URLToken_Print(t *testing.T) {
	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "fallback")
	plan, out, err := runWizard(t, "", Options{
		Yes: true, State: "turso", Print: true,
		TursoURL: "libsql://h?authToken=SECRET-88",
	}, nil)
	if err != nil {
		t.Fatal("wizard failed")
	}
	if strings.Contains(out, "SECRET-88") {
		t.Error("preview contains the embedded token")
	}
	if !strings.Contains(out, `url = "libsql://h"`) {
		t.Error("preview does not contain the cleaned state URL")
	}
	if plan.Config.State.AuthToken != "SECRET-88" || plan.ProvisionTurso {
		t.Error("embedded token was not used for the existing remote")
	}
}

func TestWizard_URLToken_Write(t *testing.T) {
	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "fallback")
	plan, out, err := runWizard(t, "", Options{
		Yes: true, State: "turso", TursoURL: "libsql://h?authToken=SECRET-88",
	}, nil)
	if err != nil {
		t.Fatal("wizard failed")
	}
	raw, err := os.ReadFile(plan.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("SECRET-88")) || bytes.Contains(raw, []byte("auth_token")) {
		t.Error("config.toml contains a credential")
	}
	if strings.Contains(out, "SECRET-88") {
		t.Error("wizard output contains the embedded token")
	}
	if !plan.ConfigWritten || plan.ProvisionTurso || plan.Config.State.URL != "libsql://h" {
		t.Error("wizard did not write the cleaned existing remote")
	}
	if plan.Config.State.AuthToken != "SECRET-88" {
		t.Error("returned token differs from the embedded token")
	}
	env, err := os.ReadFile(envFilePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "TURSO_AUTH_TOKEN=SECRET-88\n") {
		t.Error("embedded token did not reach the env file")
	}
}

func TestWizard_URLToken_FlagPrecedence(t *testing.T) {
	pinHome(t)
	t.Setenv("TURSO_AUTH_TOKEN", "fallback")
	cfg := config.Defaults()
	provision, _, token, err := chapterState(bufio.NewReader(strings.NewReader("")), &bytes.Buffer{}, Options{
		Yes: true, State: "turso", TursoURL: "libsql://h?authToken=SECRET-88", TursoAuthToken: "FLAG",
	}, cfg, false)
	if err != nil {
		t.Fatal("state chapter failed")
	}
	if token != "FLAG" || cfg.State.AuthToken != "FLAG" {
		t.Error("flag token did not take precedence")
	}
	if provision || cfg.State.URL != "libsql://h" {
		t.Error("flag token path did not retain a cleaned existing remote")
	}
}

func TestWizard_URLToken_Confirm(t *testing.T) {
	pinHome(t)
	t.Setenv("TURSO_DATABASE_URL", "libsql://h/private?authToken=SECRET-88&mode=read#fragment")
	t.Setenv("TURSO_AUTH_TOKEN", "fallback")
	var out bytes.Buffer
	cfg := config.Defaults()
	provision, _, token, err := chapterState(bufio.NewReader(strings.NewReader("y\n")), &out, Options{State: "turso"}, cfg, false)
	if err != nil {
		t.Fatal("state chapter failed")
	}
	want := "Use TURSO_DATABASE_URL/AUTH_TOKEN from your environment (libsql://h)? [Y/n]: "
	if !strings.HasPrefix(out.String(), want) || strings.Contains(out.String(), "SECRET-88") {
		t.Error("confirmation did not show only the URL scheme and host")
	}
	if provision || token != "SECRET-88" {
		t.Error("confirmation did not accept the embedded token")
	}
}

func TestWizard_URLToken_CompletePair(t *testing.T) {
	t.Setenv("TURSO_AUTH_TOKEN", "")
	for _, source := range []string{"flag", "environment"} {
		t.Run(source, func(t *testing.T) {
			opts := Options{Yes: true, State: "turso", Print: true}
			t.Setenv("TURSO_DATABASE_URL", "")
			if source == "flag" {
				opts.TursoURL = "libsql://h?authToken=SECRET-88"
			} else {
				t.Setenv("TURSO_DATABASE_URL", "libsql://h?authToken=SECRET-88")
			}
			plan, out, err := runWizard(t, "", opts, nil)
			if err != nil {
				t.Fatal("wizard failed")
			}
			if plan.ProvisionTurso || plan.Config.State.URL != "libsql://h" || plan.Config.State.AuthToken != "SECRET-88" {
				t.Error("URL with embedded token was not treated as a complete pair")
			}
			if strings.Contains(out, "SECRET-88") {
				t.Error("preview contains the embedded token")
			}
		})
	}
}

func TestWizard_URLToken_PromptDefault(t *testing.T) {
	pinHome(t)
	t.Setenv("TURSO_AUTH_TOKEN", "fallback")
	t.Setenv("TURSO_DATABASE_URL", "")
	var out bytes.Buffer
	cfg := config.Defaults()
	provision, _, token, err := chapterState(bufio.NewReader(strings.NewReader("libsql://h?token=SECRET-88\n\n")), &out, Options{State: "turso"}, cfg, false)
	if err != nil || provision || token != "SECRET-88" || cfg.State.URL != "libsql://h" {
		t.Error("prompt did not default to the embedded token and clean the URL")
	}
	if strings.Contains(out.String(), "SECRET-88") {
		t.Error("prompt output contains the embedded token")
	}
}

func TestWizard_URLToken_Malformed(t *testing.T) {
	for _, source := range []string{"flag", "environment", "prompt", "retained"} {
		for _, mode := range []string{"print", "write"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				pinHome(t)
				t.Setenv("TURSO_DATABASE_URL", "")
				t.Setenv("TURSO_AUTH_TOKEN", "fallback")
				cfgPath := filepath.Join(t.TempDir(), "config.toml")
				opts := Options{ConfigPath: cfgPath, State: "turso", Yes: true, Print: mode == "print"}
				bad := "libsql://h/%zz?authToken=SECRET-88"
				input := ""
				var existing *config.Config
				switch source {
				case "flag":
					opts.TursoURL = bad
				case "environment":
					t.Setenv("TURSO_DATABASE_URL", bad)
					opts.Yes = false
					input = "\n\n\ny\n\n\n"
				case "prompt":
					opts.Yes = false
					input = "\n\n\n" + bad + "\n\n\n\n"
				case "retained":
					existing = config.Defaults()
					existing.State.URL = bad
				}
				var out bytes.Buffer
				opts.In, opts.Out = strings.NewReader(input), &out
				_, err := Run(opts, existing)
				if err == nil || err.Error() != "invalid Turso URL" {
					t.Error("malformed URL did not produce the fixed safe error")
				}
				if strings.Contains(out.String(), "SECRET-88") {
					t.Error("malformed URL token appeared in output or a prompt")
				}
				if strings.Contains(out.String(), "Use TURSO_DATABASE_URL/AUTH_TOKEN") || strings.Contains(out.String(), "Turso auth token:") {
					t.Error("malformed URL reached a confirmation or token prompt")
				}
				if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
					t.Error("malformed URL wrote config.toml")
				}
			})
		}
	}
}

func TestWizard_URLToken_Retained(t *testing.T) {
	for _, source := range []string{"edit", "https-overlay", "local-libsql", "flag-precedence"} {
		for _, mode := range []string{"print", "write"} {
			if source == "local-libsql" && mode == "print" {
				continue
			}
			t.Run(source+"/"+mode, func(t *testing.T) {
				pinHome(t)
				t.Setenv("TURSO_DATABASE_URL", "")
				t.Setenv("TURSO_AUTH_TOKEN", "")
				cfgPath := filepath.Join(t.TempDir(), "config.toml")
				existing := config.Defaults()
				existing.State.URL = "libsql://h?authToken=SECRET-88"
				opts := Options{ConfigPath: cfgPath, Yes: true, State: "turso", Print: mode == "print"}
				wantToken := "SECRET-88"
				switch source {
				case "https-overlay":
					if err := config.Save(cfgPath, config.Defaults()); err != nil {
						t.Fatal(err)
					}
					t.Setenv("TURSO_DATABASE_URL", "https://h?authToken=SECRET-88")
					var err error
					existing, err = config.Load(cfgPath)
					if err != nil {
						t.Fatal("load failed")
					}
					opts.State = "local"
				case "local-libsql":
					opts.State = "local"
				case "flag-precedence":
					opts.TursoAuthToken, wantToken = "FLAG", "FLAG"
				}
				var out bytes.Buffer
				opts.In, opts.Out = strings.NewReader(""), &out
				plan, err := Run(opts, existing)
				if err != nil {
					t.Fatal("wizard failed")
				}
				if strings.Contains(out.String(), "SECRET-88") || strings.Contains(plan.Config.State.URL, "SECRET-88") {
					t.Error("retained URL token appeared in output or the resulting URL")
				}
				if mode == "write" {
					raw, err := os.ReadFile(cfgPath)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(raw, []byte("SECRET-88")) || bytes.Contains(raw, []byte("auth_token")) {
						t.Error("retained URL credential appeared in config.toml")
					}
					env, err := os.ReadFile(envFilePath(t))
					if err != nil {
						t.Fatal("retained URL credential did not reach the env file")
					}
					if !strings.Contains(string(env), "TURSO_AUTH_TOKEN="+wantToken+"\n") {
						t.Error("env file did not receive the expected token")
					}
				}
			})
		}
	}
}

func TestWizard_LocalStatePathUnchanged(t *testing.T) {
	for _, kind := range []string{"default", "bare"} {
		for _, directory := range []string{"with space", "with%percent"} {
			t.Run(kind+"/"+directory, func(t *testing.T) {
				pinHome(t)
				t.Setenv("TURSO_DATABASE_URL", "")
				t.Setenv("TURSO_AUTH_TOKEN", "")
				dataHome := filepath.Join(t.TempDir(), directory)
				t.Setenv("XDG_DATA_HOME", dataHome)
				wantURL := "file://" + filepath.Join(dataHome, "dotfiles", "state.db")
				var existing *config.Config
				if kind == "bare" {
					existing = config.Defaults()
					wantURL = filepath.Join(dataHome, "state.db")
					existing.State.URL = wantURL
				}
				cfgPath := filepath.Join(t.TempDir(), "config.toml")
				plan, err := Run(Options{ConfigPath: cfgPath, Yes: true, State: "local"}, existing)
				if err != nil {
					t.Fatal("valid local state path was rejected")
				}
				written, err := config.Load(cfgPath)
				if err != nil {
					t.Fatal("reading written config failed")
				}
				if !plan.ConfigWritten || plan.Config.State.URL != wantURL || written.State.URL != wantURL {
					t.Error("local state path was not preserved byte for byte")
				}
			})
		}
	}
}

func TestWizard_URLToken_RepeatedEndpoint(t *testing.T) {
	for _, endpoint := range []string{"same", "different-host", "different-path", "different-scheme"} {
		t.Run(endpoint, func(t *testing.T) {
			pinHome(t)
			t.Setenv("TURSO_DATABASE_URL", "")
			t.Setenv("TURSO_AUTH_TOKEN", "")
			existing := config.Defaults()
			existing.State.URL = "libsql://h?authToken=SECRET-88"
			flagURL := "libsql://h"
			switch endpoint {
			case "different-host":
				flagURL = "libsql://other"
			case "different-path":
				flagURL = "libsql://h/other"
			case "different-scheme":
				flagURL = "https://h"
			}
			var out bytes.Buffer
			plan, err := Run(Options{
				ConfigPath: filepath.Join(t.TempDir(), "config.toml"), Yes: true, State: "turso", TursoURL: flagURL, Out: &out,
			}, existing)
			if err != nil {
				t.Fatal("wizard failed")
			}
			if strings.Contains(out.String(), "SECRET-88") || strings.Contains(plan.Config.State.URL, "SECRET-88") {
				t.Error("retained token appeared in output or the resulting URL")
			}
			env, readErr := os.ReadFile(envFilePath(t))
			if endpoint == "same" {
				if plan.ProvisionTurso || plan.Config.State.AuthToken != "SECRET-88" || plan.Config.State.URL != flagURL {
					t.Error("repeated endpoint did not reuse the retained token without provisioning")
				}
				if readErr != nil || !strings.Contains(string(env), "TURSO_AUTH_TOKEN=SECRET-88\n") {
					t.Error("retained token did not reach the env file")
				}
			} else {
				if !plan.ProvisionTurso {
					t.Error("a different endpoint reused the retained token")
				}
				if readErr == nil || !os.IsNotExist(readErr) {
					t.Error("a different endpoint persisted the retained token")
				}
			}
		})
	}
}

func TestWizard_URLToken_LocalFlagPrecedence(t *testing.T) {
	pinHome(t)
	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "")
	cfg := config.Defaults()
	cfg.State.URL = "https://h?authToken=SECRET-88"
	var out bytes.Buffer
	_, _, token, err := chapterState(bufio.NewReader(strings.NewReader("")), &out, Options{
		Yes: true, State: "local", TursoAuthToken: "FLAG",
	}, cfg, true)
	if err != nil || token != "FLAG" {
		t.Error("explicit token did not win on the local retained path")
	}
	if cfg.State.URL != "https://h" || strings.Contains(out.String(), "SECRET-88") {
		t.Error("local retained path leaked its embedded token")
	}
}

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
	t.Setenv("PATH", t.TempDir())
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

func TestWizard_LSMChoice(t *testing.T) {
	for _, choice := range []string{"default", "decline", "explicit env", "missing", "print", "edit"} {
		t.Run(choice, func(t *testing.T) {
			pinHome(t)
			t.Setenv("TURSO_DATABASE_URL", "")
			t.Setenv("TURSO_AUTH_TOKEN", "")
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			log := filepath.Join(dir, "args")
			t.Setenv("STUB_ARGS", log)
			if choice != "missing" {
				if err := os.WriteFile(filepath.Join(dir, "lsm"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STUB_ARGS\"\n/bin/cat > /dev/null\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			opts := Options{ConfigPath: filepath.Join(t.TempDir(), "config.toml"), TursoURL: "libsql://h", TursoAuthToken: "SECRET-110", LSMApp: "custom", LSMEnv: "dev"}
			input := "\n\n\n\n\nn\n"
			if choice == "decline" {
				input = "\n\n\nn\n\nn\n"
			}
			if choice == "explicit env" || choice == "missing" {
				input = "\n\n\n\nn\n"
			}
			if choice == "explicit env" {
				opts.SecretStore = "env"
				opts.SecretStoreExplicit = true
			}
			if choice == "print" {
				opts.Print = true
				opts.SecretStore = "lsm"
				opts.Yes = true
				input = ""
			}
			var existing *config.Config
			if choice == "edit" {
				existing = config.Defaults()
				existing.State.Secret = &config.SecretRef{Provider: "lsm", App: "existing", Env: "test", Key: "CUSTOM"}
				opts.Yes = true
				opts.TursoURL = ""
				opts.TursoAuthToken = ""
				input = ""
			}
			var out bytes.Buffer
			opts.In = strings.NewReader(input)
			opts.Out = &out
			plan, err := Run(opts, existing)
			if err != nil {
				t.Fatal("wizard failed")
			}
			prompted := strings.Contains(out.String(), "Store the auth token in lsm (app custom, env dev)? [Y/n]")
			wantPrompt := choice == "default" || choice == "decline"
			if prompted != wantPrompt {
				t.Error("unexpected lsm prompt")
			}
			args, readErr := os.ReadFile(log)
			if choice == "default" {
				if readErr != nil || !strings.Contains(string(args), "custom\n--env\ndev") {
					t.Error("interactive default did not select custom lsm reference")
				}
				if plan.Config.State.Secret == nil || plan.Config.State.Secret.App != "custom" {
					t.Error("missing selected reference")
				}
			} else if !os.IsNotExist(readErr) {
				t.Error("unexpected lsm call")
			}
			if choice == "print" && (!strings.Contains(out.String(), "[state.secret]") || plan.Config.State.Secret == nil) {
				t.Error("preview omitted selected reference")
			}
			if choice == "edit" && (plan.Config.State.Secret == nil || *plan.Config.State.Secret != *existing.State.Secret) {
				t.Error("edit lost existing reference")
			}
			if choice == "decline" || choice == "explicit env" || choice == "missing" {
				data, err := os.ReadFile(config.EnvFilePath())
				if err != nil || !strings.Contains(string(data), "TURSO_AUTH_TOKEN=SECRET-110") {
					t.Error("env choice did not save token")
				}
			}
			if strings.Contains(out.String(), "SECRET-110") {
				t.Error("wizard output contains token")
			}
		})
	}
}

func TestWizard_LSMWriteOrdering(t *testing.T) {
	for _, mode := range []string{"config failure", "delete failure", "no plaintext"} {
		t.Run(mode, func(t *testing.T) {
			pinHome(t)
			t.Setenv("TURSO_AUTH_TOKEN", "")
			t.Setenv("TURSO_DATABASE_URL", "")
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			input := filepath.Join(dir, "input")
			t.Setenv("STUB_INPUT", input)
			if err := os.WriteFile(filepath.Join(dir, "lsm"), []byte("#!/bin/sh\n/bin/cat > \"$STUB_INPUT\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			envPath := config.EnvFilePath()
			if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if mode == "config failure" {
				if err := os.Mkdir(cfgPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(envPath, []byte("TURSO_AUTH_TOKEN=old\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "delete failure" {
				if err := os.Mkdir(envPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			_, err := Run(Options{ConfigPath: cfgPath, Force: true, Yes: true, TursoURL: "libsql://h", TursoAuthToken: "SECRET-110", SecretStore: "lsm", Out: &out}, nil)
			if (err != nil) != (mode != "no plaintext") {
				t.Fatal("unexpected persistence result")
			}
			stdin, readErr := os.ReadFile(input)
			if readErr != nil || string(stdin) != "SECRET-110" {
				t.Error("lsm was not saved before persistence")
			}
			if mode == "config failure" {
				data, readErr := os.ReadFile(envPath)
				if readErr != nil || string(data) != "TURSO_AUTH_TOKEN=old\n" {
					t.Error("failed config save deleted plaintext")
				}
			}
			if mode == "delete failure" {
				cfg, loadErr := config.Load(cfgPath)
				if loadErr != nil || cfg.State.Secret == nil {
					t.Error("delete failure lost persisted reference")
				}
			}
			if mode == "no plaintext" {
				if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
					t.Error("lsm setup created env file")
				}
			}
			if strings.Contains(out.String(), "removed plaintext token") {
				t.Error("removal notice printed without deleting a token")
			}
		})
	}
}

func TestWizard_LSMEOF(t *testing.T) {
	for _, input := range []string{"", "\n\n\n\t "} {
		t.Run(fmt.Sprintf("bytes_%d", len(input)), func(t *testing.T) {
			pinHome(t)
			t.Setenv("TURSO_DATABASE_URL", "")
			t.Setenv("TURSO_AUTH_TOKEN", "")
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			log := filepath.Join(dir, "called")
			t.Setenv("STUB_ARGS", log)
			if err := os.WriteFile(filepath.Join(dir, "lsm"), []byte("#!/bin/sh\nprintf called > \"$STUB_ARGS\"\n/bin/cat >/dev/null\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			envPath := config.EnvFilePath()
			if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(envPath, []byte("OTHER=keep\nTURSO_AUTH_TOKEN=old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			plan, err := Run(Options{ConfigPath: filepath.Join(t.TempDir(), "config.toml"), TursoURL: "libsql://h", TursoAuthToken: "SECRET-110", In: strings.NewReader(input)}, nil)
			if err != nil {
				t.Fatal("EOF setup failed")
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Error("EOF selected lsm without opt-in")
			}
			if plan.Config.State.Secret != nil {
				t.Error("EOF created an lsm reference")
			}
			data, err := os.ReadFile(envPath)
			if err != nil || string(data) != "OTHER=keep\nTURSO_AUTH_TOKEN=SECRET-110\n" {
				t.Error("EOF did not preserve the default env destination")
			}
		})
	}
}
