package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden files from current output")

// goldenTime keeps the marker's updated= field stable across runs.
var goldenTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// Golden files make the emitted block body reviewable in a diff rather
// than assembled from string assertions scattered across tests.
func TestRenderPathBlock_Golden(t *testing.T) {
	dirs := []string{"/a", "/b", "/c"}
	for _, tc := range []struct {
		family    string
		direction string
	}{
		{"posix", pathDirectionPrepend},
		{"posix", pathDirectionAppend},
		{"zsh", pathDirectionPrepend},
		{"zsh", pathDirectionAppend},
		{"fish", pathDirectionPrepend},
		{"fish", pathDirectionAppend},
	} {
		name := tc.family + "_" + tc.direction
		t.Run(name, func(t *testing.T) {
			id := pathMarkerID(tc.direction, dirs)
			got := renderPathBlock(tc.family, id, goldenTime, tc.direction, dirs)
			golden := filepath.Join("testdata", "path_block_"+name+".txt")
			if *updateGolden {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with -update-golden to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("block mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// The three families must be genuinely distinct; a dispatch regression
// that silently fell back to the POSIX body would still pass the
// per-family golden comparisons only if the goldens were regenerated.
func TestRenderPathBlock_FamiliesDiffer(t *testing.T) {
	dirs := []string{"/a"}
	id := pathMarkerID(pathDirectionPrepend, dirs)
	posix := renderPathBlock("posix", id, goldenTime, pathDirectionPrepend, dirs)
	zsh := renderPathBlock("zsh", id, goldenTime, pathDirectionPrepend, dirs)
	fish := renderPathBlock("fish", id, goldenTime, pathDirectionPrepend, dirs)
	if posix == zsh || posix == fish || zsh == fish {
		t.Fatalf("families rendered identically:\nposix:\n%s\nzsh:\n%s\nfish:\n%s", posix, zsh, fish)
	}
	// An unknown family is the POSIX body, which is the safe default for
	// sh and .profile targets.
	if got := renderPathBlock("", id, goldenTime, pathDirectionPrepend, dirs); got != posix {
		t.Errorf("unknown family did not fall back to posix:\n%s", got)
	}
}

// ${path:#$var} matches literally; a literal in that pattern position
// would be a glob and could drop unrelated PATH entries.
func TestBuildZshPathLine_PatternUsesVariable(t *testing.T) {
	for _, d := range []string{pathDirectionPrepend, pathDirectionAppend} {
		if got := buildZshPathLine(d); !strings.Contains(got, "${path:#$__dfm_d}") {
			t.Errorf("%s: %q does not filter through $__dfm_d", d, got)
		}
	}
}

// Marker parsing must not depend on the block body, or blocks written
// by an older renderer stop being found and get duplicated instead of
// replaced.
func TestFindPathManagedEntries_BodyAgnostic(t *testing.T) {
	dirs := []string{"/a", "/b"}
	id := pathMarkerID(pathDirectionPrepend, dirs)
	for _, family := range []string{"posix", "zsh", "fish"} {
		block := renderPathBlock(family, id, goldenTime, pathDirectionPrepend, dirs)
		entries := findPathManagedEntries([]byte("# rc\n\n" + block))
		if len(entries) != 1 {
			t.Fatalf("%s: got %d entries, want 1:\n%s", family, len(entries), block)
		}
		if entries[0].Marker.ID != id {
			t.Errorf("%s: marker id = %q, want %q", family, entries[0].Marker.ID, id)
		}
		if !equalStrings(entries[0].Marker.Dirs, dirs) {
			t.Errorf("%s: dirs = %v, want %v", family, entries[0].Marker.Dirs, dirs)
		}
	}
}

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
		if got := shellFamily(tc.shell); got != tc.aliasWant {
			t.Errorf("shellFamily(%q) = %q, want %q", tc.shell, got, tc.aliasWant)
		}
		if got := pathShellFamily(tc.shell); got != tc.pathWant {
			t.Errorf("pathShellFamily(%q) = %q, want %q", tc.shell, got, tc.pathWant)
		}
	}
}

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
		_, family, err := resolvePathTarget("", path)
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
