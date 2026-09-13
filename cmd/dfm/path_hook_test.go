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
)

// newHookEnv points HOME and XDG_CONFIG_HOME at a temp dir so no test
// can reach the developer's real rc files or fragment directory.
func newHookEnv(t *testing.T) (context.Context, string) {
	t.Helper()
	home := t.TempDir()
	env := newTestEnv(t, WithHome(home))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return config.WithContext(env.Ctx, env.Cfg), home
}

func runHookCmd(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	cmd := newPathHookCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("hook %v: %v\nout:\n%s", args, err, out.String())
	}
	return out.String()
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func writeFileBytes(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func isTracked(t *testing.T, ctx context.Context, path string) bool {
	t.Helper()
	s, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()
	_, _, err = resolveTracked(ctx, s, path)
	return err == nil
}

func TestPathHookInstall_Twice_ByteIdentical(t *testing.T) {
	ctx, home := newHookEnv(t)
	targets := []string{filepath.Join(home, ".zshenv"), filepath.Join(home, ".zprofile")}

	runHookCmd(t, ctx, "install", "--shell", "zsh")
	first := make([][]byte, len(targets))
	for i, p := range targets {
		first[i] = readFileBytes(t, p)
	}

	out := runHookCmd(t, ctx, "install", "--shell", "zsh")
	for i, p := range targets {
		if got := readFileBytes(t, p); !bytes.Equal(got, first[i]) {
			t.Errorf("%s changed on second install:\nfirst:\n%s\nsecond:\n%s", p, first[i], got)
		}
	}
	if !strings.Contains(out, "no change") {
		t.Errorf("second install did not report a no-op: %q", out)
	}
}

func TestPathHookRemove_RestoresOriginalBytes(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"no trailing newline", "export FOO=1"},
		{"trailing newline", "export FOO=1\n"},
		{"trailing blank line", "export FOO=1\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, home := newHookEnv(t)
			zshenv := filepath.Join(home, ".zshenv")
			writeFileBytes(t, zshenv, tc.content)
			before := readFileBytes(t, zshenv)

			runHookCmd(t, ctx, "install", "--shell", "zsh")
			if !bytes.Contains(readFileBytes(t, zshenv), []byte(pathHookOpenMarker)) {
				t.Fatalf("install did not write the hook")
			}

			runHookCmd(t, ctx, "remove", "--shell", "zsh")
			if got := readFileBytes(t, zshenv); !bytes.Equal(got, before) {
				t.Errorf("remove did not restore bytes:\nwant %q\ngot  %q", before, got)
			}
		})
	}
}

func TestPathHookRemove_NoHookPresent_NotAnError(t *testing.T) {
	ctx, home := newHookEnv(t)
	zshenv := filepath.Join(home, ".zshenv")
	writeFileBytes(t, zshenv, "export FOO=1\n")

	runHookCmd(t, ctx, "remove", "--shell", "zsh")

	if got := string(readFileBytes(t, zshenv)); got != "export FOO=1\n" {
		t.Errorf("file changed: %q", got)
	}
	// .zprofile was never created, and remove must not create it.
	if _, err := os.Stat(filepath.Join(home, ".zprofile")); !os.IsNotExist(err) {
		t.Errorf("remove created .zprofile: %v", err)
	}
}

func TestPathHookInstall_PreservesUserContent(t *testing.T) {
	ctx, home := newHookEnv(t)
	zshenv := filepath.Join(home, ".zshenv")
	const user = "# mine\nexport FOO=1\n"
	writeFileBytes(t, zshenv, user)

	runHookCmd(t, ctx, "install", "--shell", "zsh")
	got := readFileBytes(t, zshenv)
	if !bytes.HasPrefix(got, []byte(user)) {
		t.Errorf("user content not preserved verbatim at the head:\n%s", got)
	}
}

func TestPathHookInstall_AutoTracksRcFiles(t *testing.T) {
	ctx, home := newHookEnv(t)
	zshenv := filepath.Join(home, ".zshenv")
	zprofile := filepath.Join(home, ".zprofile")

	if isTracked(t, ctx, zshenv) {
		t.Fatal(".zshenv tracked before install")
	}
	out := runHookCmd(t, ctx, "install", "--shell", "zsh")

	for _, p := range []string{zshenv, zprofile} {
		if !isTracked(t, ctx, p) {
			t.Errorf("%s not tracked after install", p)
		}
	}
	if !strings.Contains(out, "tracked") {
		t.Errorf("output does not report the newly tracked files: %q", out)
	}
}

func TestPathHookInstall_DryRun_WritesNothing(t *testing.T) {
	ctx, home := newHookEnv(t)
	zshenv := filepath.Join(home, ".zshenv")

	out := runHookCmd(t, ctx, "install", "--shell", "zsh", "--dry-run")

	if _, err := os.Stat(zshenv); !os.IsNotExist(err) {
		t.Errorf("dry-run created .zshenv: %v", err)
	}
	if _, err := os.Stat(config.FragmentPath(pathFragmentPosixName)); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote the fragment: %v", err)
	}
	if isTracked(t, ctx, zshenv) {
		t.Error("dry-run tracked .zshenv")
	}
	if !strings.Contains(out, "would install hook in") || !strings.Contains(out, "would track") {
		t.Errorf("dry-run output does not name the work: %q", out)
	}
}

func TestPathHookInstall_FragmentNotTracked(t *testing.T) {
	ctx, home := newHookEnv(t)
	writeFileBytes(t, filepath.Join(home, ".zshrc"),
		renderPathBlock("zsh", "deadbeef", goldenTime, pathDirectionPrepend, []string{"/marker/bin"}))

	runHookCmd(t, ctx, "install", "--shell", "zsh")

	fragment := config.FragmentPath(pathFragmentPosixName)
	if !bytes.Contains(readFileBytes(t, fragment), []byte("/marker/bin")) {
		t.Error("fragment does not carry the managed dir")
	}
	if isTracked(t, ctx, fragment) {
		t.Error("generated fragment must never be tracked")
	}
}

// The bash file set is .bashrc plus whichever login file the user
// already has; .profile is the fallback when .bash_profile is absent.
func TestPathHookFiles_BashLoginFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	files, family, err := pathHookFiles("bash")
	if err != nil {
		t.Fatalf("pathHookFiles: %v", err)
	}
	if family != "posix" {
		t.Errorf("family = %q, want posix", family)
	}
	if files[1] != filepath.Join(home, ".profile") {
		t.Errorf("login file = %q, want .profile", files[1])
	}

	// Login bash reads the first that exists and stops, so the hook has
	// to follow that order rather than assume .bash_profile.
	writeFileBytes(t, filepath.Join(home, ".bash_login"), "")
	files, _, err = pathHookFiles("bash")
	if err != nil {
		t.Fatalf("pathHookFiles: %v", err)
	}
	if files[1] != filepath.Join(home, ".bash_login") {
		t.Errorf("login file = %q, want .bash_login (it shadows .profile)", files[1])
	}

	writeFileBytes(t, filepath.Join(home, ".bash_profile"), "")
	files, _, err = pathHookFiles("bash")
	if err != nil {
		t.Fatalf("pathHookFiles: %v", err)
	}
	if files[1] != filepath.Join(home, ".bash_profile") {
		t.Errorf("login file = %q, want .bash_profile (it shadows .bash_login)", files[1])
	}
}

// An explicit --shell typo must not create and write a startup file for
// a shell the user never named.
func TestPathHookInstall_UnknownShellRejected(t *testing.T) {
	ctx, home := newHookEnv(t)

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"hook", "install", "--shell", "zshh"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want an error for an unknown --shell")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("err = %v, want exit %d", err, exitResolveErr)
	}
	if _, serr := os.Stat(filepath.Join(home, ".profile")); serr == nil {
		t.Error("created ~/.profile for an unrecognised shell")
	}
}

// A stale hook body has to be replaced in place, so a future change to
// the block reaches rc files that already carry one.
func TestPathHookInstall_ReplacesStaleBody(t *testing.T) {
	ctx, home := newHookEnv(t)
	rc := filepath.Join(home, ".zshenv")
	stale := "HEAD\n\n# >>> dfm:env >>>\nOLD BODY\n# <<< dfm:env <<<\nTAIL\n"
	writeFileBytes(t, rc, stale)

	runHookCmd(t, ctx, "install", "--shell", "zsh")

	got := string(readFileBytes(t, rc))
	if strings.Contains(got, "OLD BODY") {
		t.Errorf("stale body survived:\n%s", got)
	}
	if !strings.Contains(got, "__dfm_env=") {
		t.Errorf("current body not written:\n%s", got)
	}
	if !strings.HasPrefix(got, "HEAD\n") || !strings.HasSuffix(got, "TAIL\n") {
		t.Errorf("surrounding user content disturbed:\n%s", got)
	}
}

// Removing a hook that sits between user content must leave exactly the
// user content, with no blank line of its own left behind.
func TestPathHookRemove_MidFileRestoresBytes(t *testing.T) {
	ctx, home := newHookEnv(t)
	rc := filepath.Join(home, ".zshenv")
	writeFileBytes(t, rc, "HEAD\nTAIL\n")

	runHookCmd(t, ctx, "install", "--shell", "zsh")
	if got := string(readFileBytes(t, rc)); !strings.Contains(got, "dfm:env") {
		t.Fatalf("install did not write a hook:\n%s", got)
	}
	runHookCmd(t, ctx, "remove", "--shell", "zsh")

	if got := string(readFileBytes(t, rc)); got != "HEAD\nTAIL\n" {
		t.Errorf("after remove = %q, want %q", got, "HEAD\nTAIL\n")
	}
}

// An rc file holding something that looks like a secret is ordinary.
// Install must refuse it by default and proceed under --force, and the
// refusal has to name a flag this command actually has.
func TestPathHookInstall_SecretInRcFile(t *testing.T) {
	ctx, home := newHookEnv(t)
	zshenv := filepath.Join(home, ".zshenv")
	writeFileBytes(t, zshenv, "export ACME_API_KEY=sk-live-0123456789abcdef\n")

	cmd := newPathHookCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"install", "--shell", "zsh"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("want a refusal for an rc file tripping the secret scan\nout:\n%s", out.String())
	}
	if strings.Contains(err.Error(), "dfm track") {
		t.Errorf("error points at another command: %v", err)
	}
	if isTracked(t, ctx, zshenv) {
		t.Error("refused install still tracked the file")
	}

	runHookCmd(t, ctx, "install", "--shell", "zsh", "--force")
	if !bytes.Contains(readFileBytes(t, zshenv), []byte(pathHookOpenMarker)) {
		t.Error("--force did not install the hook")
	}
}
