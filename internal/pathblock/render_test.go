package pathblock

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
		{"posix", DirectionPrepend},
		{"posix", DirectionAppend},
		{"zsh", DirectionPrepend},
		{"zsh", DirectionAppend},
		{"fish", DirectionPrepend},
		{"fish", DirectionAppend},
	} {
		name := tc.family + "_" + tc.direction
		t.Run(name, func(t *testing.T) {
			id := MarkerID(tc.direction, dirs)
			got := Render(tc.family, id, goldenTime, tc.direction, dirs)
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
	id := MarkerID(DirectionPrepend, dirs)
	posix := Render("posix", id, goldenTime, DirectionPrepend, dirs)
	zsh := Render("zsh", id, goldenTime, DirectionPrepend, dirs)
	fish := Render("fish", id, goldenTime, DirectionPrepend, dirs)
	if posix == zsh || posix == fish || zsh == fish {
		t.Fatalf("families rendered identically:\nposix:\n%s\nzsh:\n%s\nfish:\n%s", posix, zsh, fish)
	}
	// An unknown family is the POSIX body, which is the safe default for
	// sh and .profile targets.
	if got := Render("", id, goldenTime, DirectionPrepend, dirs); got != posix {
		t.Errorf("unknown family did not fall back to posix:\n%s", got)
	}
}

// ${path:#$var} matches literally; a literal in that pattern position
// would be a glob and could drop unrelated PATH entries.
func TestBuildZshPathLine_PatternUsesVariable(t *testing.T) {
	for _, d := range []string{DirectionPrepend, DirectionAppend} {
		if got := BuildZshLine(d); !strings.Contains(got, "${path:#$__dfm_d}") {
			t.Errorf("%s: %q does not filter through $__dfm_d", d, got)
		}
	}
}

// Marker parsing must not depend on the block body, or blocks written
// by an older renderer stop being found and get duplicated instead of
// replaced.
func TestFindPathManagedEntries_BodyAgnostic(t *testing.T) {
	dirs := []string{"/a", "/b"}
	id := MarkerID(DirectionPrepend, dirs)
	for _, family := range []string{"posix", "zsh", "fish"} {
		block := Render(family, id, goldenTime, DirectionPrepend, dirs)
		entries := FindEntries([]byte("# rc\n\n" + block))
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
