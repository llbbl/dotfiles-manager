package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// goldenTime keeps the marker's updated= field stable across runs.
var goldenTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// --file without --shell has to infer the shell from the filename, and
// a .zshrc must reach the zsh renderer.
func TestResolvePathTarget_InfersShellFromFilename(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		base string
		want string
	}{
		{".zshrc", "zsh"},
		{".zshenv", "zsh"},
		{".zprofile", "zsh"},
		{"config.fish", "fish"},
		{".bashrc", "posix"},
		{".profile", "posix"},
		{"fixture.txt", "posix"},
	} {
		path := filepath.Join(dir, tc.base)
		_, family, err := resolvePathTarget(context.Background(), "", path)
		if err != nil {
			t.Fatalf("resolvePathTarget(%q): %v", tc.base, err)
		}
		if family != tc.want {
			t.Errorf("resolvePathTarget(%q) family = %q, want %q", tc.base, family, tc.want)
		}
		// Aliases keep their two-way collapse regardless of the above.
		_, aliasFamily, err := resolveAliasTarget("", path)
		if err != nil {
			t.Fatalf("resolveAliasTarget(%q): %v", tc.base, err)
		}
		wantAlias := "posix"
		if tc.want == "fish" {
			wantAlias = "fish"
		}
		if aliasFamily != wantAlias {
			t.Errorf("resolveAliasTarget(%q) family = %q, want %q", tc.base, aliasFamily, wantAlias)
		}
	}
}
