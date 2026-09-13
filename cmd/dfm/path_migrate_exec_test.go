package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// After a migrate the rc file no longer carries the managed block, so
// anything on PATH came from the fragment the hooks source. The login
// row is the one that needs .zprofile: macOS /etc/zprofile runs
// path_helper and rebuilds PATH after .zshenv has already run.
func TestPathMigrate_Zsh_FragmentReachesPATH(t *testing.T) {
	zsh := lookInterpreter(t, "zsh")
	ctx, home := newMigrateEnv(t)
	marker := pathDirs(t, "marker")["marker"]
	rc := seedZshrc(t, home, marker)

	runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")

	if got := readFileBytes(t, rc); bytes.Contains(got, []byte("dfm:path:")) {
		t.Fatalf("managed block still in the rc file:\n%s", got)
	}

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

// A dir added after the migration has to reach PATH the same way the
// migrated ones do — it lands in the fragment, not the rc file.
func TestPathMigrate_AddAfterwards_ReachesPATH(t *testing.T) {
	zsh := lookInterpreter(t, "zsh")
	ctx, home := newMigrateEnv(t)
	d := pathDirs(t, "one", "extra")
	seedZshrc(t, home, d["one"])

	runPathCmd(t, ctx, "migrate", "--shell", "zsh", "--yes")
	runPathCmd(t, ctx, "add", d["extra"])

	got := strings.Split(hookPATH(t, home, zsh, "-c", `echo "$PATH"`), ":")
	for _, want := range []string{d["one"], d["extra"]} {
		found := false
		for _, p := range got {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q not on PATH: %v", want, got)
		}
	}
}
