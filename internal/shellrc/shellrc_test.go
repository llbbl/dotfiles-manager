package shellrc

import (
	"path/filepath"
	"testing"
)

// PATH rendering needs zsh split out, while alias rendering must keep
// collapsing it into posix — the two resolutions share a file resolver
// and it would be easy to widen the wrong one.
func TestShellFamilies_ZshSplitsForPathOnly(t *testing.T) {
	for _, tc := range []struct {
		shell     string
		aliasWant string
		pathWant  string
	}{
		{"bash", "posix", "posix"},
		{"zsh", "posix", "zsh"},
		{"sh", "posix", "posix"},
		{"profile", "posix", "posix"},
		{"", "posix", "posix"},
		{"fish", "fish", "fish"},
	} {
		if got := Family(tc.shell); got != tc.aliasWant {
			t.Errorf("Family(%q) = %q, want %q", tc.shell, got, tc.aliasWant)
		}
		if got := PathFamily(tc.shell); got != tc.pathWant {
			t.Errorf("PathFamily(%q) = %q, want %q", tc.shell, got, tc.pathWant)
		}
	}
}

func TestRCFile_MapsShellToRCFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct {
		shell string
		want  string
	}{
		{"bash", filepath.Join(home, ".bashrc")},
		{"zsh", filepath.Join(home, ".zshrc")},
		{"fish", filepath.Join(home, ".config", "fish", "config.fish")},
		{"tcsh", filepath.Join(home, ".profile")},
	} {
		got, err := RCFile(tc.shell)
		if err != nil {
			t.Fatalf("RCFile(%q): %v", tc.shell, err)
		}
		if got != tc.want {
			t.Errorf("RCFile(%q) = %q, want %q", tc.shell, got, tc.want)
		}
	}
}

func TestFromFilename_GuessesShell(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/x/config.fish", "fish"},
		{"/x/.zshrc", "zsh"},
		{"/x/.ZSHRC", "zsh"},
		{"/x/zshenv", "zsh"},
		{"/x/.zprofile", "zsh"},
		{"/x/.bashrc", "sh"},
		{"/x/.profile", "sh"},
	} {
		if got := FromFilename(tc.path); got != tc.want {
			t.Errorf("FromFilename(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestResolveTarget_FlagsAndShellEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	file := filepath.Join(home, "custom.zshrc")
	for _, tc := range []struct {
		name      string
		env       string
		shellFlag string
		fileFlag  string
		wantPath  string
		wantShell string
	}{
		{"file infers shell", "/bin/bash", "", file, file, "zsh"},
		{"file with explicit shell", "/bin/bash", "fish", file, file, "fish"},
		{"relative file made absolute", "/bin/bash", "", "./.zshrc", filepath.Join(home, ".zshrc"), "zsh"},
		{"shell flag beats env", "/bin/bash", "zsh", "", filepath.Join(home, ".zshrc"), "zsh"},
		{"env only", "/usr/local/bin/fish", "", "", filepath.Join(home, ".config", "fish", "config.fish"), "fish"},
		{"env unset", "", "", "", filepath.Join(home, ".profile"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", tc.env)
			path, shell, err := ResolveTarget(tc.shellFlag, tc.fileFlag)
			if err != nil {
				t.Fatalf("ResolveTarget: %v", err)
			}
			if path != tc.wantPath || shell != tc.wantShell {
				t.Errorf("ResolveTarget(%q, %q) = (%q, %q), want (%q, %q)",
					tc.shellFlag, tc.fileFlag, path, shell, tc.wantPath, tc.wantShell)
			}
		})
	}
}

func TestDetect_LowercasesBasename(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string
	}{
		{"/opt/homebrew/bin/ZSH", "zsh"},
		{"", ""},
	} {
		t.Setenv("SHELL", tc.env)
		if got := Detect(); got != tc.want {
			t.Errorf("Detect() with SHELL=%q = %q, want %q", tc.env, got, tc.want)
		}
	}
}
