package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// newMigrateEnv is newHookEnv plus the two globals a migrate reads.
// $SHELL picks the fragment family; clearing the --config flag sends
// activeConfigPath through the temp XDG_CONFIG_HOME newHookEnv set, so
// nothing can reach the developer's own shell or config.
func newMigrateEnv(t *testing.T) (context.Context, string) {
	t.Helper()
	ctx, home := newHookEnv(t)
	t.Setenv("SHELL", "/bin/zsh")
	prev := flagConfigPath
	flagConfigPath = ""
	t.Cleanup(func() { flagConfigPath = prev })
	return ctx, home
}

func runPathCmd(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("path %v: %v\nout:\n%s", args, err, out.String())
	}
	return out.String()
}

// seedZshrc writes a .zshrc carrying user content plus one managed
// prepend block, the shape a migrate is expected to find.
func seedZshrc(t *testing.T, home string, dirs ...string) string {
	t.Helper()
	rc := filepath.Join(home, ".zshrc")
	writeFileBytes(t, rc, zshrcUserContent+"\n"+renderPathBlock("zsh",
		pathMarkerID(pathDirectionPrepend, dirs), goldenTime, pathDirectionPrepend, dirs))
	return rc
}

const zshrcUserContent = "# mine\nexport FOO=1\n"

func trackFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	canonical, display, err := tracker.Resolve(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	s, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()
	if _, err := tracker.Track(ctx, s, canonical, display, tracker.TrackOptions{SkipSecretCheck: true}); err != nil {
		t.Fatalf("track %s: %v", path, err)
	}
}

func fragmentPath() string { return config.FragmentPath(pathFragmentPosixName) }

// The hazard this whole change exists to close: writePathFragment used
// to regenerate the fragment from the rc file unconditionally, so a hook
// install after a migrate would overwrite the user's entire PATH
// configuration with an empty file.
func TestPathMigrate_HookInstallAfterwards_KeepsFragment(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one", "two")
	dirs := []string{d["one"], d["two"]}
	seedZshrc(t, home, dirs...)

	runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")

	before := readFileBytes(t, fragmentPath())
	for _, dir := range dirs {
		if !bytes.Contains(before, []byte(dir)) {
			t.Fatalf("migrate did not put %q in the fragment:\n%s", dir, before)
		}
	}

	out := runHookCmd(t, ctx, "install", "--shell", "zsh")

	after := readFileBytes(t, fragmentPath())
	for _, dir := range dirs {
		if !bytes.Contains(after, []byte(dir)) {
			t.Errorf("hook install lost %q from the migrated fragment:\n%s\nout:\n%s", dir, after, out)
		}
	}
	if !bytes.Equal(before, after) {
		t.Errorf("hook install rewrote the migrated fragment:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestPathMigrate_MovesBlocksAndFlipsSwitch(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one")
	rc := seedZshrc(t, home, d["one"])

	out := runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")

	if got := readFileBytes(t, rc); bytes.Contains(got, []byte("dfm:path:")) {
		t.Errorf("managed block survived in the rc file:\n%s", got)
	}
	if !bytes.Contains(readFileBytes(t, fragmentPath()), []byte(d["one"])) {
		t.Error("fragment does not carry the managed dir")
	}
	for _, f := range []string{".zshenv", ".zprofile"} {
		if !bytes.Contains(readFileBytes(t, filepath.Join(home, f)), []byte(pathHookOpenMarker)) {
			t.Errorf("%s has no hook", f)
		}
	}
	if !isTracked(t, ctx, fragmentPath()) {
		t.Error("the migrated fragment must be tracked")
	}
	if cfg := config.FromContext(ctx); !cfg.Path.UseFragment {
		t.Error("use_fragment not flipped in the loaded config")
	}
	saved, err := config.Load(mustConfigPath(t))
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !saved.Path.UseFragment {
		t.Error("use_fragment not persisted to config.toml")
	}
	if !strings.Contains(out, "set path.use_fragment = true") {
		t.Errorf("output does not report the config write: %q", out)
	}
}

func mustConfigPath(t *testing.T) string {
	t.Helper()
	p, err := activeConfigPath()
	if err != nil {
		t.Fatalf("activeConfigPath: %v", err)
	}
	return p
}

// Everything outside the managed block is the user's, and a migrate has
// to hand it back unchanged — including the blank line an add wrote as a
// separator in front of the block.
func TestPathMigrate_LeavesUserContentByteIdentical(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one")
	rc := seedZshrc(t, home, d["one"])

	runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")

	if got := string(readFileBytes(t, rc)); got != zshrcUserContent {
		t.Errorf("rc file after migrate = %q, want %q", got, zshrcUserContent)
	}
}

func TestPathMigrate_PathAddTargetsFragment(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one", "extra")
	rc := seedZshrc(t, home, d["one"])

	runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")
	rcAfterMigrate := readFileBytes(t, rc)

	runPathCmd(t, ctx, "add", d["extra"])

	if got := readFileBytes(t, fragmentPath()); !bytes.Contains(got, []byte(d["extra"])) {
		t.Errorf("path add did not write into the fragment:\n%s", got)
	}
	if got := readFileBytes(t, rc); !bytes.Equal(got, rcAfterMigrate) {
		t.Errorf("path add touched the rc file:\n%s", got)
	}
}

// --file is the escape hatch and keeps working after a migrate.
func TestPathMigrate_PathAddFileStillWins(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one", "extra")
	rc := seedZshrc(t, home, d["one"])

	runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")
	runPathCmd(t, ctx, "add", "--file", rc, d["extra"])

	if got := readFileBytes(t, rc); !bytes.Contains(got, []byte(d["extra"])) {
		t.Errorf("--file did not target the rc file:\n%s", got)
	}
	if got := readFileBytes(t, fragmentPath()); bytes.Contains(got, []byte(d["extra"])) {
		t.Errorf("--file wrote into the fragment as well:\n%s", got)
	}
}

func TestPathMigrate_PathListReportsFragment(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one")
	seedZshrc(t, home, d["one"])

	runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")

	if out := runPathCmd(t, ctx, "list"); !strings.Contains(out, d["one"]) {
		t.Errorf("path list does not report the fragment's entries: %q", out)
	}
}

func TestPathMigrate_DryRun_WritesNothing(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one")
	rc := seedZshrc(t, home, d["one"])
	before := readFileBytes(t, rc)

	out := runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--dry-run")

	for _, p := range []string{fragmentPath(), filepath.Join(home, ".zshenv"),
		filepath.Join(home, ".zprofile"), mustConfigPath(t)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("dry-run wrote %s: %v", p, err)
		}
	}
	if got := readFileBytes(t, rc); !bytes.Equal(got, before) {
		t.Errorf("dry-run edited the rc file:\n%s", got)
	}
	if cfg := config.FromContext(ctx); cfg.Path.UseFragment {
		t.Error("dry-run flipped use_fragment")
	}
	for _, want := range []string{"would write fragment", "would install hook in",
		"would track", "would strip 1 managed block(s)", "would set path.use_fragment = true"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output is missing %q:\n%s", want, out)
		}
	}
}

func TestPathMigrate_Twice_IsANoOp(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one")
	seedZshrc(t, home, d["one"])

	runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")
	before := readFileBytes(t, fragmentPath())

	out := runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")

	if !strings.Contains(out, "already migrated") {
		t.Errorf("second migrate did not report a no-op: %q", out)
	}
	if got := readFileBytes(t, fragmentPath()); !bytes.Equal(got, before) {
		t.Errorf("second migrate rewrote the fragment:\n%s", got)
	}
}

// Without --yes and with no TTY to prompt on, a migrate refuses rather
// than mutating the user's startup files unasked. A pipe read-end is how
// the import tests spell "not a TTY".
func TestPathMigrate_NonInteractiveWithoutYes_Refuses(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one")
	seedZshrc(t, home, d["one"])

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(r)
	cmd.SetArgs([]string{"migrate", "--shell", "zsh"})

	var ee *exitError
	if err := cmd.Execute(); !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("err = %v, want exit %d", err, exitResolveErr)
	}
	if _, serr := os.Stat(fragmentPath()); !os.IsNotExist(serr) {
		t.Error("refused migrate still wrote the fragment")
	}
}

// Answering anything but y at the prompt leaves every file alone.
func TestPathMigrate_Declined_WritesNothing(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one")
	rc := seedZshrc(t, home, d["one"])
	before := readFileBytes(t, rc)

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs([]string{"migrate", "--shell", "zsh"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !strings.Contains(out.String(), "declined") {
		t.Errorf("output does not report the decline: %q", out.String())
	}
	if got := readFileBytes(t, rc); !bytes.Equal(got, before) {
		t.Errorf("declined migrate edited the rc file:\n%s", got)
	}
	if _, err := os.Stat(fragmentPath()); !os.IsNotExist(err) {
		t.Errorf("declined migrate wrote the fragment: %v", err)
	}
	if cfg := config.FromContext(ctx); cfg.Path.UseFragment {
		t.Error("declined migrate flipped use_fragment")
	}
}

// The whole no-breaking-change claim: a user who never migrates gets
// exactly the pre-migrate behaviour.
func TestPathAdd_WithoutMigrate_TargetsRcFile(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one")
	rc := filepath.Join(home, ".zshrc")
	writeFileBytes(t, rc, zshrcUserContent)
	trackFile(t, ctx, rc)

	runPathCmd(t, ctx, "add", d["one"])

	if got := readFileBytes(t, rc); !bytes.Contains(got, []byte(d["one"])) {
		t.Errorf("path add did not write into the rc file:\n%s", got)
	}
	if _, err := os.Stat(fragmentPath()); !os.IsNotExist(err) {
		t.Errorf("path add wrote a fragment without a migrate: %v", err)
	}
	if _, err := os.Stat(mustConfigPath(t)); !os.IsNotExist(err) {
		t.Errorf("path add wrote config.toml: %v", err)
	}
}

// A CRLF rc file gets the same byte-identical promise as an LF one: the
// blank separator an add wrote is "\r\n" there, not "\n".
func TestRemovePathBlocks_CRLFSeparator(t *testing.T) {
	for _, tc := range []struct{ name, eol string }{
		{"lf", "\n"},
		{"crlf", "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := "# mine" + tc.eol + "export EDITOR=vim" + tc.eol
			block := renderPathBlock("posix", pathMarkerID(pathDirectionPrepend, []string{"/a"}),
				goldenTime, pathDirectionPrepend, []string{"/a"})
			if tc.eol == "\r\n" {
				block = strings.ReplaceAll(block, "\n", "\r\n")
			}
			content := []byte(head + tc.eol + block)

			got := removePathBlocks(content, findPathManagedEntries(content))
			if string(got) != head {
				t.Errorf("after remove = %q, want %q", got, head)
			}
		})
	}
}

// The switch must be persisted before the rc file is stripped. A crash
// in the other order leaves the blocks gone from the rc file while the
// flag still says "not migrated", and the next hook install regenerates
// the fragment from a file that no longer has them.
func TestPathMigrate_FlipsSwitchBeforeStripping(t *testing.T) {
	ctx, home := newMigrateEnv(t)
	seedZshrc(t, home, "/a")

	out := runPathCmd(t, ctx, "migrate", "--yes")

	flip := strings.Index(out, "set path.use_fragment = true")
	strip := strings.Index(out, "stripped 1 managed block(s)")
	if flip < 0 || strip < 0 {
		t.Fatalf("migrate output missing a step:\n%s", out)
	}
	if flip > strip {
		t.Errorf("config flip reported after the rc strip; ordering is load-bearing:\n%s", out)
	}
}

// config.Load overlays TURSO_AUTH_TOKEN, so saving the struct back could
// persist an env-only token. Blanking it unconditionally is equally
// wrong: omitempty would then delete a token the file legitimately owns.
func TestPathMigrate_AuthTokenHandling(t *testing.T) {
	for _, tc := range []struct {
		name, fileToken, envToken string
		wantInEnvFile             bool
	}{
		{"env only", "", "env-secret", false},
		{"file only", "file-secret", "", true},
		{"both", "file-secret", "env-secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, home := newMigrateEnv(t)
			seedZshrc(t, home, "/a")

			cfgPath, err := activeConfigPath()
			if err != nil {
				t.Fatalf("activeConfigPath: %v", err)
			}
			if tc.fileToken != "" {
				cfg := config.Defaults()
				cfg.State.AuthToken = tc.fileToken
				if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := config.Save(cfgPath, cfg); err != nil {
					t.Fatalf("seed config: %v", err)
				}
			}
			t.Setenv("TURSO_AUTH_TOKEN", tc.envToken)

			out := runPathCmd(t, ctx, "migrate", "--yes")

			got, err := config.SavedAuthToken(cfgPath)
			if err != nil {
				t.Fatalf("SavedAuthToken: %v", err)
			}
			if got != "" {
				t.Error("config.toml still carries an auth_token")
			}

			envPath := config.EnvFilePath()
			if !tc.wantInEnvFile {
				if _, err := os.Stat(envPath); !os.IsNotExist(err) {
					t.Errorf("env file created for an env-only token (err=%v)", err)
				}
				if strings.Contains(out, "moved state.auth_token") {
					t.Error("migration notice printed with nothing to migrate")
				}
				return
			}
			env, err := os.ReadFile(envPath)
			if err != nil {
				t.Fatalf("env file: %v", err)
			}
			if !strings.Contains(string(env), "TURSO_AUTH_TOKEN="+tc.fileToken) {
				t.Error("file token not migrated to the env file")
			}
			if n := strings.Count(out, "moved state.auth_token"); n != 1 {
				t.Errorf("migration notice printed %d times, want 1", n)
			}
		})
	}
}
