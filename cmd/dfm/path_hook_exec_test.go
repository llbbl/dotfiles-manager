package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hookPATH runs argv under a hooked HOME and returns the PATH it
// produced. The env is pinned to HOME, XDG_CONFIG_HOME and PATH so a
// stray variable from the developer's session cannot influence it.
func hookPATH(t *testing.T, home string, argv ...string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"PATH=/usr/bin:/bin",
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v\nstderr:\n%s", argv, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// installZshHooks builds a temp HOME whose .zshrc carries one managed
// PATH entry, then installs the hooks and the fragment from it.
func installZshHooks(t *testing.T) (home, marker string) {
	t.Helper()
	ctx, home := newHookEnv(t)
	marker = pathDirs(t, "marker")["marker"]
	writeFileBytes(t, filepath.Join(home, ".zshrc"), renderPathBlock("zsh",
		pathMarkerID(pathDirectionPrepend, []string{marker}), goldenTime,
		pathDirectionPrepend, []string{marker}))
	runHookCmd(t, ctx, "install", "--shell", "zsh")
	return home, marker
}

// The login row is why zsh gets two hook files: macOS /etc/zprofile
// runs path_helper, which rebuilds PATH after .zshenv has run. Neither
// the login shell nor the script reads .zshrc, so the managed dir can
// only have come from the fragment.
func TestPathHook_Zsh_FragmentReachesPATH(t *testing.T) {
	zsh := lookInterpreter(t, "zsh")
	home, marker := installZshHooks(t)

	script := filepath.Join(t.TempDir(), "probe.zsh")
	writeFileBytes(t, script, "#!"+zsh+"\necho \"$PATH\"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	cases := []struct {
		name string
		argv []string
	}{
		{"non-login", []string{zsh, "-c", `echo "$PATH"`}},
		{"login", []string{zsh, "-lc", `echo "$PATH"`}},
		{"script", []string{script}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Split(hookPATH(t, home, tc.argv...), ":")
			if got[0] != marker {
				t.Errorf("PATH[0] = %q, want %q\nfull PATH: %v", got[0], marker, got)
			}
		})
	}
}

// fish's rc file is also its hook file, so the managed block is dropped
// after the fragment is generated; anything left on PATH came from the
// hook sourcing env.fish.
func TestPathHook_Fish_FragmentReachesPATH(t *testing.T) {
	fish := lookInterpreter(t, "fish")
	ctx, home := newHookEnv(t)
	marker := pathDirs(t, "marker")["marker"]
	configFish := filepath.Join(home, ".config", "fish", "config.fish")
	writeFileBytes(t, configFish, renderPathBlock("fish",
		pathMarkerID(pathDirectionPrepend, []string{marker}), goldenTime,
		pathDirectionPrepend, []string{marker}))

	runHookCmd(t, ctx, "install", "--shell", "fish")
	writeFileBytes(t, configFish, renderPathHook("fish"))

	got := strings.Fields(hookPATH(t, home, fish, "-c", "echo $PATH"))
	if len(got) == 0 || got[0] != marker {
		t.Errorf("PATH[0] = %v, want %q", got, marker)
	}
	count := 0
	for _, p := range got {
		if p == marker {
			count++
		}
	}
	if count != 1 {
		t.Errorf("managed dir appears %d times, want 1: %v", count, got)
	}
}

// A hook whose fragment does not exist is a no-op, not an error. The
// `[ -r ]` / `test -r` guard is what makes that true; runPathBlock
// fails the test on a non-zero exit or any stderr output.
func TestPathHook_MissingFragment_LeavesPATHUntouched(t *testing.T) {
	dirs := pathDirs(t, "a", "b", "c")
	keep := mapDirs(dirs, []string{"a", "b", "c"})
	start := strings.Join(keep, ":")

	for _, sh := range posixInterpreters {
		t.Run(strings.TrimPrefix(sh, "/bin/"), func(t *testing.T) {
			p := lookInterpreter(t, sh)
			got := runPathBlock(t, pathInterpreter{
				name:      sh,
				argv:      []string{p},
				echoCmd:   `echo "$PATH"`,
				sourceCmd: ".",
				split:     splitColon,
			}, renderPathHook("posix"), start, keep, 1)
			if !equalStrings(got, keep) {
				t.Errorf("PATH = %v, want %v", got, keep)
			}
		})
	}

	t.Run("zsh", func(t *testing.T) {
		zsh := lookInterpreter(t, "zsh")
		got := runPathBlock(t, pathInterpreter{
			name:      "zsh",
			argv:      []string{zsh, "-f"},
			echoCmd:   `echo "$PATH"`,
			sourceCmd: ".",
			split:     splitColon,
		}, renderPathHook("posix"), start, keep, 1)
		if !equalStrings(got, keep) {
			t.Errorf("PATH = %v, want %v", got, keep)
		}
	})

	t.Run("fish", func(t *testing.T) {
		fish := lookInterpreter(t, "fish")
		got := runPathBlock(t, pathInterpreter{
			name:      "fish",
			argv:      []string{fish},
			echoCmd:   `echo $PATH`,
			sourceCmd: "source",
			split:     splitFields,
		}, renderPathHook("fish"), start, keep, 1)
		if !equalStrings(got, keep) {
			t.Errorf("PATH = %v, want %v", got, keep)
		}
	})
}
