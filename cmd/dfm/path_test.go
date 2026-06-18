package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestPathMarkerID_Determinism(t *testing.T) {
	id1 := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	id2 := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	if id1 != id2 {
		t.Errorf("same input produced different ids: %q vs %q", id1, id2)
	}
}

func TestPathMarkerID_ExactlyEightHex(t *testing.T) {
	id := pathMarkerID(pathDirectionPrepend, []string{"/a"})
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
		{pathDirectionPrepend, pathDirectionPrepend, []string{"/a"}, []string{"/b"}},
		{pathDirectionPrepend, pathDirectionPrepend, []string{"/a", "/b"}, []string{"/a"}},
		// Same dirs, different direction → different ids. This is the
		// invariant that lets one rc file hold a prepend AND an append
		// entry over the same dirs without id collision.
		{pathDirectionPrepend, pathDirectionAppend, []string{"/a"}, []string{"/a"}},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			ida := pathMarkerID(tc.a, tc.aDir)
			idb := pathMarkerID(tc.b, tc.bDir)
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
			direction := pathDirectionPrepend
			updated := time.Date(2026, 6, 17, 12, 34, 56, 0, time.UTC)
			id := pathMarkerID(direction, tc.dirs)
			line := formatPathOpenMarker(id, updated, direction, tc.dirs)

			got, ok := parsePathOpenMarker(line)
			if !ok {
				t.Fatalf("parsePathOpenMarker returned false for %q", line)
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
		if _, ok := parsePathOpenMarker(c); ok {
			t.Errorf("parsePathOpenMarker(%q) = true, want false", c)
		}
	}
}

func TestPathCloseMarker_RoundTrip(t *testing.T) {
	id := pathMarkerID(pathDirectionAppend, []string{"/a", "/b"})
	line := formatPathCloseMarker(id)
	got, ok := parsePathCloseMarker(line)
	if !ok {
		t.Fatalf("parsePathCloseMarker returned false for %q", line)
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
		if _, ok := parsePathCloseMarker(c); ok {
			t.Errorf("parsePathCloseMarker(%q) = true, want false", c)
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
			got := normalizePathDir(tc.in)
			if got != tc.want {
				t.Errorf("normalizePathDir(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizePathDir_CrossSpellingEquivalence(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	a := normalizePathDir("~/foo")
	b := normalizePathDir("$HOME/foo")
	c := normalizePathDir(filepath.Join(tmp, "foo"))
	d := normalizePathDir(filepath.Join(tmp, "foo") + "/")
	if a != b || b != c || c != d {
		t.Errorf("expected all equal:\n  ~/foo    = %q\n  $HOME/foo= %q\n  literal  = %q\n  trailing = %q",
			a, b, c, d)
	}
}

// fixture helper: emit a well-formed block (open marker + arbitrary
// body + close marker, all newline-terminated) with the given direction
// and dirs. Body is opaque to the parser — it's the shell-specific
// for-loop in production, but the helper doesn't care.
func wellFormedPathBlock(direction string, dirs []string, body string) string {
	id := pathMarkerID(direction, dirs)
	open := formatPathOpenMarker(id, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC), direction, dirs)
	close := formatPathCloseMarker(id)
	return open + "\n" + body + close + "\n"
}

func TestFindPathManagedEntries_TwoWellFormedEntries(t *testing.T) {
	prepend := wellFormedPathBlock(
		pathDirectionPrepend,
		[]string{"/a", "/b"},
		"for __dfm_d in /a /b; do : ; done\n",
	)
	appendBlk := wellFormedPathBlock(
		pathDirectionAppend,
		[]string{"/c"},
		"for __dfm_d in /c; do : ; done\n",
	)
	content := []byte("# bashrc\n" + prepend + "\n# user stuff\nexport FOO=1\n" + appendBlk)

	entries := findPathManagedEntries(content)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2:\n%s", len(entries), content)
	}

	// First entry: prepend.
	if entries[0].Marker.Direction != pathDirectionPrepend {
		t.Errorf("entry 0 direction: got %q, want %q", entries[0].Marker.Direction, pathDirectionPrepend)
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
	if _, ok := parsePathOpenMarker(openLine); !ok {
		t.Errorf("entry 0 open-line bytes don't reparse: %q", openLine)
	}
	closeLine := string(content[entries[0].CloseStart:entries[0].CloseEnd])
	if id, ok := parsePathCloseMarker(closeLine); !ok || id != entries[0].Marker.ID {
		t.Errorf("entry 0 close-line bytes don't reparse: %q", closeLine)
	}

	// Second entry: append.
	if entries[1].Marker.Direction != pathDirectionAppend {
		t.Errorf("entry 1 direction: got %q, want %q", entries[1].Marker.Direction, pathDirectionAppend)
	}
	if got, want := []string{"/c"}, entries[1].Marker.Dirs; !equalStrings(got, want) {
		t.Errorf("entry 1 dirs: got %v, want %v", got, want)
	}
	if string(content[entries[1].BlockStart:entries[1].BlockEnd]) != appendBlk {
		t.Errorf("entry 1 block bytes don't match fixture block")
	}
}

func TestFindPathManagedEntries_UnpairedOpenIgnored(t *testing.T) {
	id := pathMarkerID(pathDirectionPrepend, []string{"/a"})
	open := formatPathOpenMarker(id, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC), pathDirectionPrepend, []string{"/a"})
	content := []byte("# bashrc\n" + open + "\nfor __dfm_d in /a; do : ; done\n# no close marker\n")

	entries := findPathManagedEntries(content)
	if len(entries) != 0 {
		t.Fatalf("got %d entries for unpaired open, want 0:\n%s", len(entries), content)
	}
}

func TestFindPathManagedEntries_MismatchedIDsIgnored(t *testing.T) {
	openID := pathMarkerID(pathDirectionPrepend, []string{"/a"})
	open := formatPathOpenMarker(
		openID, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
		pathDirectionPrepend, []string{"/a"},
	)
	// Close marker with an id that doesn't match the open. Use a
	// different deterministic id to keep the regex happy.
	closeID := pathMarkerID(pathDirectionAppend, []string{"/z"})
	close := formatPathCloseMarker(closeID)

	content := []byte("# bashrc\n" + open + "\nbody\n" + close + "\n")
	entries := findPathManagedEntries(content)
	if len(entries) != 0 {
		t.Fatalf("got %d entries for mismatched id open/close, want 0:\n%s", len(entries), content)
	}
}

func TestFindPathManagedEntries_EmptyInput(t *testing.T) {
	if entries := findPathManagedEntries(nil); entries != nil {
		t.Errorf("got %v, want nil for nil input", entries)
	}
	if entries := findPathManagedEntries([]byte("")); entries != nil {
		t.Errorf("got %v, want nil for empty input", entries)
	}
	if entries := findPathManagedEntries([]byte("# bashrc\nexport FOO=1\n")); entries != nil {
		t.Errorf("got %v, want nil for content with no markers", entries)
	}
}

// Stubs were retired in dfm-mxf / dfm-2bl / dfm-5mq. The corresponding
// behavior is now covered by path_add_fish_test.go, path_remove_test.go,
// and path_list_test.go.

// equalStrings is a tiny helper to compare two string slices by value.
// Avoids pulling in a dep for one line.
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

// Sanity check: ensure os.UserHomeDir is what t.Setenv-toggled HOME
// implies. This is a guard against future Go versions changing
// resolution and silently breaking normalizePathDir's equivalence.
func TestUserHomeDirRespectsHOME(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != tmp {
		t.Errorf("os.UserHomeDir = %q, want %q (the HOME we set)", got, tmp)
	}
}
