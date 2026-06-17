package main

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPathMarkerID_Deterministic(t *testing.T) {
	const dir = "/Users/me/.local/bin"
	a := pathMarkerID(dir)
	b := pathMarkerID(dir)
	if a != b {
		t.Fatalf("pathMarkerID not deterministic: %q vs %q", a, b)
	}
}

func TestPathMarkerID_DifferentInputs(t *testing.T) {
	a := pathMarkerID("/Users/me/.local/bin")
	b := pathMarkerID("/Users/me/.local/sbin")
	if a == b {
		t.Fatalf("pathMarkerID collided on distinct inputs: both = %q", a)
	}
}

func TestPathMarkerID_Shape(t *testing.T) {
	hex8 := regexp.MustCompile(`^[0-9a-f]{8}$`)
	cases := []string{
		"/usr/local/bin",
		"$HOME/.local/bin",
		"~/foo",
		"",
		"with spaces/and=equals",
	}
	for _, in := range cases {
		got := pathMarkerID(in)
		if !hex8.MatchString(got) {
			t.Errorf("pathMarkerID(%q) = %q, want 8 hex chars", in, got)
		}
	}
}

func TestFormatParsePathMarker_RoundTrip(t *testing.T) {
	stamp := time.Date(2026, 6, 17, 14, 30, 45, 0, time.UTC)
	cases := []struct {
		name string
		dir  string
	}{
		{"plain abs", "/usr/local/bin"},
		{"env ref", "$HOME/.local/bin"},
		{"tilde", "~/foo"},
		{"with equals", "/opt/weird=path/bin"},
		{"trailing slash literal", "/usr/local/bin/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := pathMarkerID(tc.dir)
			line := formatPathMarker(id, tc.dir, stamp)

			// Sanity: the rendered line carries the ISO 8601 stamp
			// verbatim so a human grep'ing the rc file can see when
			// the entry was added.
			if !strings.Contains(line, "added=2026-06-17T14:30:45Z") {
				t.Errorf("rendered line missing ISO stamp: %q", line)
			}

			gotID, gotDir, ok := parsePathMarker(line)
			if !ok {
				t.Fatalf("parsePathMarker failed on its own output: %q", line)
			}
			if gotID != id {
				t.Errorf("id round-trip: got %q want %q", gotID, id)
			}
			if gotDir != tc.dir {
				t.Errorf("dir round-trip: got %q want %q", gotDir, tc.dir)
			}
		})
	}
}

func TestParsePathMarker_RejectsNonMarkers(t *testing.T) {
	cases := []string{
		"",
		"# just a comment",
		"export PATH=\"/foo:$PATH\"",
		"# dfm:alias foo >>>",
		"# dfm:path:short  added=2026  dir=/foo", // id too short
		"# dfm:path:ZZZZZZZZ  added=2026 dir=/foo", // non-hex id
	}
	for _, line := range cases {
		if _, _, ok := parsePathMarker(line); ok {
			t.Errorf("parsePathMarker accepted non-marker line: %q", line)
		}
	}
}

func TestNormalizePathDir(t *testing.T) {
	// Pin $HOME so the ~/$HOME/abs equivalence is deterministic
	// regardless of the host's actual home dir.
	t.Setenv("HOME", "/Users/foo")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty", "", ""},
		{"bare root stays root", "/", "/"},
		{"trailing slash stripped", "/usr/local/bin/", "/usr/local/bin"},
		{"multiple trailing slashes stripped", "/usr/local/bin///", "/usr/local/bin"},
		{"tilde to abs home", "~/foo", "/Users/foo/foo"},
		{"$HOME to abs home", "$HOME/foo", "/Users/foo/foo"},
		{"abs already-home form", "/Users/foo/foo", "/Users/foo/foo"},
		{"bare $HOME to abs home", "$HOME", "/Users/foo"},
		{"unrelated absolute path untouched", "/opt/bin", "/opt/bin"},
		{"trailing slash plus tilde", "~/foo/", "/Users/foo/foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizePathDir(tc.in)
			if got != tc.want {
				t.Errorf("normalizePathDir(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizePathDir_EquivalenceUnderHome(t *testing.T) {
	t.Setenv("HOME", "/Users/foo")
	// All three of these refer to the same directory on disk;
	// normalize must collapse them to one form.
	forms := []string{
		"~/foo",
		"$HOME/foo",
		"/Users/foo/foo",
		"/Users/foo/foo/", // trailing slash variant
	}
	canonical := normalizePathDir(forms[0])
	for _, f := range forms[1:] {
		got := normalizePathDir(f)
		if got != canonical {
			t.Errorf("normalizePathDir(%q) = %q, want %q (canonical from %q)",
				f, got, canonical, forms[0])
		}
	}
}

func TestNormalizePathDir_NoHomeEnv_LeavesEnvRefIntact(t *testing.T) {
	// If $HOME is unset we still strip the trailing slash and rewrite
	// ~/ to $HOME/, but we do NOT expand $HOME to an absolute path.
	// Better to over-add than silently rewrite to the wrong place.
	t.Setenv("HOME", "")
	got := normalizePathDir("~/foo/")
	if got != "$HOME/foo" {
		t.Errorf("normalizePathDir without $HOME = %q, want %q", got, "$HOME/foo")
	}
}
