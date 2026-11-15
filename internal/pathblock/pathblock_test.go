package pathblock

import (
	"fmt"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestPathMarkerID_Determinism(t *testing.T) {
	id1 := MarkerID(DirectionPrepend, []string{"/a", "/b"})
	id2 := MarkerID(DirectionPrepend, []string{"/a", "/b"})
	if id1 != id2 {
		t.Errorf("same input produced different ids: %q vs %q", id1, id2)
	}
}

func TestPathMarkerID_ExactlyEightHex(t *testing.T) {
	id := MarkerID(DirectionPrepend, []string{"/a"})
	if len(id) != 8 {
		t.Errorf("id length = %d, want 8 (id=%q)", len(id), id)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(id) {
		t.Errorf("id is not 8 hex chars: %q", id)
	}
}

func TestPathMarkerID_DifferentInputsProduceDifferentIDs(t *testing.T) {
	cases := []struct {
		a, b string
		aDir []string
		bDir []string
	}{
		{DirectionPrepend, DirectionPrepend, []string{"/a"}, []string{"/b"}},
		{DirectionPrepend, DirectionPrepend, []string{"/a", "/b"}, []string{"/a"}},
		// Same dirs, different direction → different ids. This is the
		// invariant that lets one rc file hold a prepend AND an append
		// entry over the same dirs without id collision.
		{DirectionPrepend, DirectionAppend, []string{"/a"}, []string{"/a"}},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			ida := MarkerID(tc.a, tc.aDir)
			idb := MarkerID(tc.b, tc.bDir)
			if ida == idb {
				t.Errorf("expected different ids for (%s, %v) vs (%s, %v), both = %q",
					tc.a, tc.aDir, tc.b, tc.bDir, ida)
			}
		})
	}
}

func TestPathOpenMarker_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		dirs []string
	}{
		{"simple", []string{"/usr/local/bin", "/opt/homebrew/bin"}},
		{"single dir", []string{"/a"}},
		// Spaces and equals signs in dir tokens are unusual but legal
		// in POSIX paths. The marker emits them verbatim and the
		// parser must round-trip them.
		{"weird chars",
			[]string{"/home/me/has space", "/tmp/key=value", "/x"}},
		{"empty", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			direction := DirectionPrepend
			updated := time.Date(2026, 6, 17, 12, 34, 56, 0, time.UTC)
			id := MarkerID(direction, tc.dirs)
			line := FormatOpen(id, updated, direction, tc.dirs)

			got, ok := ParseOpen(line)
			if !ok {
				t.Fatalf("pathblock.ParseOpen returned false for %q", line)
			}
			if got.ID != id {
				t.Errorf("ID: got %q, want %q", got.ID, id)
			}
			if !got.UpdatedAt.Equal(updated) {
				t.Errorf("UpdatedAt: got %s, want %s", got.UpdatedAt, updated)
			}
			if got.Direction != direction {
				t.Errorf("Direction: got %q, want %q", got.Direction, direction)
			}
			// nil vs empty-slice: normalize for comparison.
			if len(got.Dirs) != len(tc.dirs) {
				t.Fatalf("Dirs len: got %d (%v), want %d (%v)",
					len(got.Dirs), got.Dirs, len(tc.dirs), tc.dirs)
			}
			for i, d := range tc.dirs {
				if got.Dirs[i] != d {
					t.Errorf("Dirs[%d]: got %q, want %q", i, got.Dirs[i], d)
				}
			}
		})
	}
}

func TestPathOpenMarker_RejectsNonsense(t *testing.T) {
	cases := []string{
		"# not a marker",
		"# dfm:alias:xxxxxxxx >>>  ...",
		"# dfm:path:short >>>",
		"",
	}
	for _, c := range cases {
		if _, ok := ParseOpen(c); ok {
			t.Errorf("pathblock.ParseOpen(%q) = true, want false", c)
		}
	}
}

func TestPathCloseMarker_RoundTrip(t *testing.T) {
	id := MarkerID(DirectionAppend, []string{"/a", "/b"})
	line := FormatClose(id)
	got, ok := ParseClose(line)
	if !ok {
		t.Fatalf("pathblock.ParseClose returned false for %q", line)
	}
	if got != id {
		t.Errorf("id: got %q, want %q", got, id)
	}
}

func TestPathCloseMarker_RejectsNonsense(t *testing.T) {
	cases := []string{
		"# dfm:path:xxxxxxxx >>>",
		"# random comment",
		"",
		"# dfm:path:nothex8 <<<",
	}
	for _, c := range cases {
		if _, ok := ParseClose(c); ok {
			t.Errorf("pathblock.ParseClose(%q) = true, want false", c)
		}
	}
}

func TestNormalizePathDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cases := []struct {
		in   string
		want string
	}{
		// Trailing slash stripped.
		{"/usr/local/bin/", "/usr/local/bin"},
		{"/usr/local/bin", "/usr/local/bin"},
		// Multiple trailing slashes all go.
		{"/foo///", "/foo"},
		// Root preserved.
		{"/", "/"},
		// $HOME / ~ equivalence to literal HOME.
		{"~/foo", filepath.Join(tmp, "foo")},
		{"$HOME/foo", filepath.Join(tmp, "foo")},
		{filepath.Join(tmp, "foo"), filepath.Join(tmp, "foo")},
		{"~", tmp},
		{"$HOME", tmp},
		// No tilde / no $HOME: pass through (with trailing slash strip).
		{"/opt/x/", "/opt/x"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := NormalizeDir(tc.in)
			if got != tc.want {
				t.Errorf("pathblock.NormalizeDir(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizePathDir_CrossSpellingEquivalence(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	a := NormalizeDir("~/foo")
	b := NormalizeDir("$HOME/foo")
	c := NormalizeDir(filepath.Join(tmp, "foo"))
	d := NormalizeDir(filepath.Join(tmp, "foo") + "/")
	if a != b || b != c || c != d {
		t.Errorf("expected all equal:\n  ~/foo    = %q\n  $HOME/foo= %q\n  literal  = %q\n  trailing = %q",
			a, b, c, d)
	}
}

func wellFormedPathBlock(direction string, dirs []string, body string) string {
	id := MarkerID(direction, dirs)
	open := FormatOpen(id, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC), direction, dirs)
	close := FormatClose(id)
	return open + "\n" + body + close + "\n"
}

func TestFindPathManagedEntries_TwoWellFormedEntries(t *testing.T) {
	prepend := wellFormedPathBlock(
		DirectionPrepend,
		[]string{"/a", "/b"},
		"for __dfm_d in /a /b; do : ; done\n",
	)
	appendBlk := wellFormedPathBlock(
		DirectionAppend,
		[]string{"/c"},
		"for __dfm_d in /c; do : ; done\n",
	)
	content := []byte("# bashrc\n" + prepend + "\n# user stuff\nexport FOO=1\n" + appendBlk)

	entries := FindEntries(content)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2:\n%s", len(entries), content)
	}

	// First entry: prepend.
	if entries[0].Marker.Direction != DirectionPrepend {
		t.Errorf("entry 0 direction: got %q, want %q", entries[0].Marker.Direction, DirectionPrepend)
	}
	if got, want := []string{"/a", "/b"}, entries[0].Marker.Dirs; !equalStrings(got, want) {
		t.Errorf("entry 0 dirs: got %v, want %v", got, want)
	}
	// Offsets must round-trip: slicing content[BlockStart:BlockEnd] is exactly the block text + trailing \n.
	if string(content[entries[0].BlockStart:entries[0].BlockEnd]) != prepend {
		t.Errorf("entry 0 block bytes don't match fixture block")
	}
	// Open/close line bytes must round-trip (without trailing \n).
	openLine := string(content[entries[0].OpenStart:entries[0].OpenEnd])
	if _, ok := ParseOpen(openLine); !ok {
		t.Errorf("entry 0 open-line bytes don't reparse: %q", openLine)
	}
	closeLine := string(content[entries[0].CloseStart:entries[0].CloseEnd])
	if id, ok := ParseClose(closeLine); !ok || id != entries[0].Marker.ID {
		t.Errorf("entry 0 close-line bytes don't reparse: %q", closeLine)
	}

	// Second entry: append.
	if entries[1].Marker.Direction != DirectionAppend {
		t.Errorf("entry 1 direction: got %q, want %q", entries[1].Marker.Direction, DirectionAppend)
	}
	if got, want := []string{"/c"}, entries[1].Marker.Dirs; !equalStrings(got, want) {
		t.Errorf("entry 1 dirs: got %v, want %v", got, want)
	}
	if string(content[entries[1].BlockStart:entries[1].BlockEnd]) != appendBlk {
		t.Errorf("entry 1 block bytes don't match fixture block")
	}
}

func TestFindPathManagedEntries_UnpairedOpenIgnored(t *testing.T) {
	id := MarkerID(DirectionPrepend, []string{"/a"})
	open := FormatOpen(id, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC), DirectionPrepend, []string{"/a"})
	content := []byte("# bashrc\n" + open + "\nfor __dfm_d in /a; do : ; done\n# no close marker\n")

	entries := FindEntries(content)
	if len(entries) != 0 {
		t.Fatalf("got %d entries for unpaired open, want 0:\n%s", len(entries), content)
	}
}

func TestFindPathManagedEntries_MismatchedIDsIgnored(t *testing.T) {
	openID := MarkerID(DirectionPrepend, []string{"/a"})
	open := FormatOpen(
		openID, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
		DirectionPrepend, []string{"/a"},
	)
	// Close marker with an id that doesn't match the open. Use a
	// different deterministic id to keep the regex happy.
	closeID := MarkerID(DirectionAppend, []string{"/z"})
	close := FormatClose(closeID)

	content := []byte("# bashrc\n" + open + "\nbody\n" + close + "\n")
	entries := FindEntries(content)
	if len(entries) != 0 {
		t.Fatalf("got %d entries for mismatched id open/close, want 0:\n%s", len(entries), content)
	}
}

func TestFindPathManagedEntries_EmptyInput(t *testing.T) {
	if entries := FindEntries(nil); entries != nil {
		t.Errorf("got %v, want nil for nil input", entries)
	}
	if entries := FindEntries([]byte("")); entries != nil {
		t.Errorf("got %v, want nil for empty input", entries)
	}
	if entries := FindEntries([]byte("# bashrc\nexport FOO=1\n")); entries != nil {
		t.Errorf("got %v, want nil for content with no markers", entries)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
