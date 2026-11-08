package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func TestInit_Print_URLToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "fallback")
	prev := flagConfigPath
	flagConfigPath = filepath.Join(home, "config.toml")
	t.Cleanup(func() { flagConfigPath = prev })

	cmd := newInitCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--yes", "--state", "turso", "--turso-url", "libsql://h?authToken=SECRET-88", "--print"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal("init preview failed")
	}
	if strings.Contains(out.String(), "SECRET-88") {
		t.Error("init preview contains the embedded token")
	}
	if !strings.Contains(out.String(), `url = "libsql://h"`) {
		t.Error("init preview does not contain the cleaned state URL")
	}
	if _, err := os.Stat(flagConfigPath); !os.IsNotExist(err) {
		t.Error("init preview wrote config.toml")
	}
}

// TestInit_HappyPathFreshConfig drives the cobra `dfm init` command end
// to end in non-interactive mode and verifies the wizard wrote a usable
// config.toml at the requested path with the documented defaults.
func TestInit_HappyPathFreshConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// The cobra command reads flagConfigPath (a package-level var set
	// by the persistent --config flag). Set it directly since we're
	// bypassing the root command's flag parsing.
	prev := flagConfigPath
	flagConfigPath = cfgPath
	t.Cleanup(func() { flagConfigPath = prev })

	cmd := newInitCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--yes"})

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init --yes: %v\noutput: %s", err, out.String())
	}

	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
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
}

// TestInit_ChapterFive_TracksInline pins the chapter-5 yes-track
// integration: when the wizard returns a Plan with TrackPath != "",
// the cobra layer must actually track the file (via runTrackOne) so
// the user doesn't have to re-run `dfm track` after init. This is the
// PR-#43 follow-up — the prior implementation only printed a hint.
//
// We exercise the cobra-layer hookup directly: call runTrackOne with
// the same ctx and arguments init.go uses (rawPath = plan.TrackPath,
// zero-value trackOneOptions), then verify a tracked_files row
// landed in the state DB.
func TestInit_ChapterFive_TracksInline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// chapterTrack hard-codes "~/.zshrc" — mirror that exact rawPath
	// here so the test fails if the chapter's contract drifts.
	rcPath := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rcPath, []byte("# test rc\n"), 0o644); err != nil {
		t.Fatalf("write ~/.zshrc: %v", err)
	}

	ctx, _, _ := setupEditCmdEnv(t)

	cmd := newInitCmd()
	cmd.SetContext(ctx)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	code, err := runTrackOne(cmd, "~/.zshrc", trackOneOptions{})
	if err != nil {
		t.Fatalf("runTrackOne: %v\nstderr: %s", err, errOut.String())
	}
	if code != 0 {
		t.Fatalf("runTrackOne code = %d, want 0\nstderr: %s", code, errOut.String())
	}

	// Resolve to canonical so the WHERE clause matches the row inserted
	// by tracker.Track (which keys on canonical paths).
	canonical, err := filepath.EvalSymlinks(rcPath)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}

	s, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()

	var (
		displayPath string
	)
	row := s.DB().QueryRowContext(ctx,
		`SELECT display_path FROM tracked_files WHERE path = ?`, canonical)
	if err := row.Scan(&displayPath); err != nil {
		t.Fatalf("expected tracked_files row for %s: %v", canonical, err)
	}
	if displayPath == "" {
		t.Errorf("display_path is empty for tracked row")
	}

	if !bytes.Contains(out.Bytes(), []byte("tracked")) {
		t.Errorf("expected 'tracked' confirmation on stdout, got: %s", out.String())
	}
}

func TestInit_LSM(t *testing.T) {
	for _, mode := range []string{"success", "failure", "print"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			t.Setenv("TURSO_DATABASE_URL", "")
			t.Setenv("TURSO_AUTH_TOKEN", "")
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			log, input := filepath.Join(dir, "args"), filepath.Join(dir, "input")
			t.Setenv("STUB_ARGS", log)
			t.Setenv("STUB_INPUT", input)
			t.Setenv("STUB_MODE", mode)
			script := `#!/bin/sh
printf '%s\n' "$@" >> "$STUB_ARGS"
printf 'LEAK-STDERR' >&2
if [ "$STUB_MODE" = failure ]; then printf 'LEAK-STDOUT'; exit 1; fi
/bin/cat > "$STUB_INPUT"
printf 'LEAK-STDOUT'
`
			if err := os.WriteFile(filepath.Join(dir, "lsm"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			envPath := config.EnvFilePath()
			if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
				t.Fatal(err)
			}
			seeded := "OTHER=keep\nTURSO_AUTH_TOKEN=old\n"
			if err := os.WriteFile(envPath, []byte(seeded), 0o600); err != nil {
				t.Fatal(err)
			}
			prev := flagConfigPath
			flagConfigPath, _ = config.DefaultPath()
			t.Cleanup(func() { flagConfigPath = prev })
			cmd := newInitCmd()
			cmd.SetContext(context.Background())
			args := []string{"--turso-url", "libsql://h", "--turso-auth-token", "SECRET-110", "--secret-store", "lsm", "--yes"}
			if mode == "print" {
				args = append(args, "--print")
			}
			cmd.SetArgs(args)
			var out, errOut bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			err := cmd.Execute()
			if (err != nil) != (mode == "failure") {
				t.Fatal("unexpected init result")
			}
			for _, s := range []string{out.String(), errOut.String()} {
				if strings.Contains(s, "SECRET-110") || strings.Contains(s, "LEAK-") {
					t.Error("sensitive output reached command streams")
				}
			}
			if err != nil && (strings.Contains(err.Error(), "SECRET-110") || strings.Contains(err.Error(), "LEAK-")) {
				t.Error("sensitive output reached error")
			}
			data, readErr := os.ReadFile(flagConfigPath)
			if mode != "success" {
				if !os.IsNotExist(readErr) {
					t.Error("config written during failure or preview")
				}
			} else if readErr != nil || !strings.Contains(string(data), "[state.secret]") || strings.Contains(string(data), "SECRET-110") {
				t.Error("config lacks reference or contains token")
			}
			env, readErr := os.ReadFile(envPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "success" {
				if string(env) != "OTHER=keep\n" {
					t.Error("plaintext token was not removed")
				}
				if !strings.Contains(out.String(), "removed plaintext token from "+envPath) {
					t.Error("missing removal notice")
				}
			} else if string(env) != seeded {
				t.Error("env changed during failure or preview")
			}
			argv, argvErr := os.ReadFile(log)
			if mode == "print" {
				if !os.IsNotExist(argvErr) {
					t.Error("preview called lsm")
				}
				if !strings.Contains(out.String(), "[state.secret]") {
					t.Error("preview omitted reference")
				}
				return
			}
			if argvErr != nil || string(argv) != "set\n--app\ndfm\n--env\nprod\nTURSO_AUTH_TOKEN\n-\n" || strings.Contains(string(argv), "SECRET-110") {
				t.Error("unexpected lsm argv")
			}
			if mode == "success" {
				stdin, err := os.ReadFile(input)
				if err != nil || string(stdin) != "SECRET-110" {
					t.Error("token did not reach stdin exactly")
				}
			}
		})
	}
}

func TestInitLSMRootHelper(t *testing.T) {
	if os.Getenv("DFM_LSM_ROOT_HELPER") != "1" {
		return
	}
	sep := 0
	for i, arg := range os.Args {
		if arg == "--" {
			sep = i + 1
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	root := newRootCmd()
	root.SetContext(ctx)
	root.SetArgs(os.Args[sep:])
	err := root.Execute()
	_ = closeActiveResources()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func setupLSMRoot(t *testing.T) (home, dir, log, input string) {
	t.Helper()
	home = t.TempDir()
	dir = t.TempDir()
	for key, value := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"), "XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_CACHE_HOME": filepath.Join(home, "cache"), "TURSO_GO_CACHE_DIR": filepath.Join(home, "native"), "TURSO_DATABASE_URL": "", "TURSO_AUTH_TOKEN": "", "DFM_LOG_LEVEL": "debug", "DFM_LOG_DEST": "stderr", "DFM_ENV_FILE": "", "DFM_LSM_ROOT_HELPER": "1", "PATH": dir} {
		t.Setenv(key, value)
	}
	log = filepath.Join(dir, "args")
	input = filepath.Join(dir, "input")
	t.Setenv("STUB_ARGS", log)
	t.Setenv("STUB_INPUT", input)
	t.Setenv("STUB_MODE", "")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$STUB_ARGS"
printf LEAK-STDERR >&2
if [ "$STUB_MODE" = failure ]; then printf LEAK-STDOUT; exit 1; fi
if [ "$1" = set ]; then /bin/cat > "$STUB_INPUT"; else printf SECRET-110; fi
`
	if err := os.WriteFile(filepath.Join(dir, "lsm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return
}

func runLSMRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestInitLSMRootHelper$", "--", "--no-dotenv"}, args...)...)
	data, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("root subprocess exceeded test deadline")
	}
	return string(data), err
}

func TestInit_LSMRootInspection(t *testing.T) {
	for _, args := range [][]string{{"config", "show"}, {"config", "path"}, {"version"}, {"init", "--print", "--yes"}, {"list", "--json"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			_, _, log, _ := setupLSMRoot(t)
			t.Setenv("STUB_MODE", "failure")
			cfg := config.Defaults()
			cfg.State.URL = "libsql://127.0.0.1:1"
			cfg.State.Secret = &config.SecretRef{Provider: "lsm", App: "dfm", Env: "test"}
			path, _ := config.DefaultPath()
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			output, err := runLSMRoot(t, args...)
			if args[0] == "list" {
				if err == nil {
					t.Error("state consumer unexpectedly succeeded with failing lsm")
				}
				data, readErr := os.ReadFile(log)
				if readErr != nil || !strings.Contains(string(data), "get\n") {
					t.Error("state consumer skipped lsm lookup")
				}
			} else {
				if err != nil {
					t.Error("inspection failed")
				}
				if _, readErr := os.Stat(log); !os.IsNotExist(readErr) {
					t.Error("inspection or preview invoked lsm")
				}
				if args[0] == "config" && args[1] == "show" && !strings.Contains(output, "[state.secret]") {
					t.Error("startup suppression removed reference from command config")
				}
			}
			if strings.Contains(output, "SECRET-110") || strings.Contains(output, "LEAK-") {
				t.Error("subprocess output or token escaped to CLI logs")
			}
		})
	}
}

func TestInit_LSMProvisioning(t *testing.T) {
	for _, mode := range []string{"success", "failure", "missing"} {
		t.Run(mode, func(t *testing.T) {
			_, dir, log, input := setupLSMRoot(t)
			t.Setenv("STUB_MODE", mode)
			if mode == "missing" {
				if err := os.Remove(filepath.Join(dir, "lsm")); err != nil {
					t.Fatal(err)
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			_ = listener.Close()
			turso := "#!/bin/sh\ncase \"$*\" in\n'auth whoami') exit 0;;\n'db show dotfiles-state') exit 0;;\n'db show dotfiles-state --url') printf 'libsql://" + address + "';;\n'db tokens create dotfiles-state') printf SECRET-110;;\n*) exit 1;;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "turso"), []byte(turso), 0o700); err != nil {
				t.Fatal(err)
			}
			envPath := config.EnvFilePath()
			if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
				t.Fatal(err)
			}
			seeded := "OTHER=keep\nTURSO_AUTH_TOKEN=old\n"
			if err := os.WriteFile(envPath, []byte(seeded), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := runLSMRoot(t, "init", "--yes", "--turso", "--secret-store", "lsm", "--lsm-app", "custom", "--lsm-env", "test")
			if err == nil {
				t.Error("expected controlled failure before real remote use")
			}
			path, _ := config.DefaultPath()
			cfg, loadErr := config.Load(path)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			env, readErr := os.ReadFile(envPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "success" {
				if cfg.State.Secret == nil || cfg.State.Secret.App != "custom" || cfg.State.Secret.Env != "test" {
					t.Error("provisioned token has no selected lsm reference")
				}
				if string(env) != "OTHER=keep\n" {
					t.Error("provisioned token retained plaintext")
				}
				data, readErr := os.ReadFile(input)
				if readErr != nil || string(data) != "SECRET-110" {
					t.Error("provisioned token did not reach lsm stdin exactly")
				}
			} else {
				if cfg.State.Secret != nil {
					t.Error("failed lsm write saved a reference")
				}
				if string(env) != seeded {
					t.Error("failed lsm write changed plaintext file")
				}
			}
			argv, _ := os.ReadFile(log)
			if strings.Contains(string(argv), "SECRET-110") || strings.Contains(output, "SECRET-110") || strings.Contains(output, "LEAK-") {
				t.Error("provisioning exposed sensitive output")
			}
			_ = filepath.WalkDir(filepath.Dir(path), func(p string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					t.Error("cannot inspect fixture config directory")
					return walkErr
				}
				if !d.IsDir() {
					data, readErr := os.ReadFile(p)
					if readErr != nil {
						t.Error("cannot read fixture config file")
					} else if bytes.Contains(data, []byte("SECRET-110")) {
						t.Error("token reached config directory")
					}
				}
				return nil
			})
		})
	}
}

func TestInit_LSMRootEOF(t *testing.T) {
	_, _, log, _ := setupLSMRoot(t)
	envPath := config.EnvFilePath()
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("OTHER=keep\nTURSO_AUTH_TOKEN=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runLSMRoot(t, "init", "--turso-url", "libsql://h", "--turso-auth-token", "SECRET-110")
	if err != nil {
		t.Fatal("EOF init failed")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("root EOF selected lsm without opt-in")
	}
	path, _ := config.DefaultPath()
	cfg, err := config.Load(path)
	if err != nil || cfg.State.Secret != nil {
		t.Error("root EOF saved an lsm reference")
	}
	data, err := os.ReadFile(envPath)
	if err != nil || string(data) != "OTHER=keep\nTURSO_AUTH_TOKEN=SECRET-110\n" {
		t.Error("root EOF changed default storage")
	}
	if strings.Contains(output, "SECRET-110") {
		t.Error("EOF init exposed token")
	}
}
