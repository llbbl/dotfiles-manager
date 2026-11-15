package main

import (
	"os"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/pathblock"
)

// fixture helper: emit a well-formed block (open marker + arbitrary
// body + close marker, all newline-terminated) with the given direction
// and dirs. Body is opaque to the parser — it's the shell-specific
// for-loop in production, but the helper doesn't care.
func wellFormedPathBlock(direction string, dirs []string, body string) string {
	id := pathblock.MarkerID(direction, dirs)
	open := pathblock.FormatOpen(id, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC), direction, dirs)
	close := pathblock.FormatClose(id)
	return open + "\n" + body + close + "\n"
}

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
// resolution and silently breaking pathblock.NormalizeDir's equivalence.
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
