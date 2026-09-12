package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// A .zshrc target renders the zsh body, not the POSIX one.
func TestPathAdd_Zsh_RendersZshBody(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, ".zshrc", "# zshrc\n")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, frag := range []string{
		"for __dfm_d in /a; do\n",
		"  path=($__dfm_d ${path:#$__dfm_d})\n",
		"unset __dfm_d\n",
	} {
		if !bytes.Contains(got, []byte(frag)) {
			t.Errorf("expected fragment %q in:\n%s", frag, got)
		}
	}
	// The POSIX guard and the explicit export belong to bash only.
	for _, frag := range []string{`case ":$PATH:" in`, "export PATH\n"} {
		if bytes.Contains(got, []byte(frag)) {
			t.Errorf("unexpected posix fragment %q in zsh output:\n%s", frag, got)
		}
	}
	if entries := findPathManagedEntries(got); len(entries) != 1 {
		t.Fatalf("got %d entries, want 1:\n%s", len(entries), got)
	}
}

func TestPathAdd_Zsh_AppendDirection(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, ".zshrc", "")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "--file", canonical, "--append", "/a"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, _ := os.ReadFile(canonical)
	if !bytes.Contains(got, []byte("  path=(${path:#$__dfm_d} $__dfm_d)\n")) {
		t.Errorf("missing append-direction zsh assignment in:\n%s", got)
	}
}

// Migration: a .zshrc carrying a block written by the old renderer must
// be replaced in place, not joined by a second entry. Parsing is
// body-agnostic precisely so this works.
func TestPathAdd_Zsh_ReplacesLegacyPosixBlock(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)

	legacyID := pathMarkerID(pathDirectionPrepend, []string{"/a"})
	legacy := renderPathBlockBash(legacyID, time.Now().UTC().Add(-time.Hour), pathDirectionPrepend, []string{"/a"})
	canonical, _ := writeTrackedNamed(t, ctx, ".zshrc", "# zshrc\n\n"+legacy+"\nexport EDITOR=vi\n")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "/b"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("got %d managed entries, want 1:\n%s", len(entries), got)
	}
	wantID := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	if entries[0].Marker.ID != wantID {
		t.Errorf("marker id = %q, want %q", entries[0].Marker.ID, wantID)
	}
	// No orphaned fragment of the old POSIX body left behind.
	for _, frag := range []string{`case ":$PATH:" in`, "esac", "export PATH", legacyID} {
		if bytes.Contains(got, []byte(frag)) {
			t.Errorf("legacy fragment %q survived:\n%s", frag, got)
		}
	}
	if !bytes.Contains(got, []byte("  path=($__dfm_d ${path:#$__dfm_d})\n")) {
		t.Errorf("zsh body not written:\n%s", got)
	}
	// Surrounding content is untouched.
	if !bytes.Contains(got, []byte("# zshrc\n")) || !bytes.Contains(got, []byte("export EDITOR=vi\n")) {
		t.Errorf("surrounding rc content disturbed:\n%s", got)
	}
	if n := bytes.Count(got, []byte("for __dfm_d in")); n != 1 {
		t.Errorf("found %d for-loops, want 1:\n%s", n, got)
	}
}

// The full add → source cycle, executed by a real zsh: the managed dirs
// land on PATH once each and re-sourcing does not move them.
func TestPathAdd_Zsh_IdempotencyOnResource(t *testing.T) {
	zsh := lookInterpreter(t, "zsh")

	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, ".zshrc", "")
	dirs := pathDirs(t, "a", "b")
	managed := []string{dirs["a"], dirs["b"]}

	for _, dir := range managed {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"add", "--file", canonical, dir})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("add %s: %v", dir, err)
		}
	}

	rc, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read rc: %v", err)
	}
	interp := pathInterpreter{
		name:      "zsh",
		argv:      []string{zsh, "-f"},
		echoCmd:   `echo "$PATH"`,
		sourceCmd: ".",
		split:     splitColon,
	}
	once := runPathBlock(t, interp, string(rc), "/usr/bin:/bin", managed, 1)
	// Both dirs sit in front of the starting PATH; the second add
	// prepends over the first.
	want := []string{dirs["b"], dirs["a"]}
	if !equalStrings(once, want) {
		t.Fatalf("after one source PATH = %v, want %v", once, want)
	}
	thrice := runPathBlock(t, interp, string(rc), "/usr/bin:/bin", managed, 3)
	if !equalStrings(thrice, want) {
		t.Errorf("after three sources PATH = %v, want %v", thrice, want)
	}
}

// `dfm path remove` on a zsh target shrinks the same block rather than
// falling back to a POSIX body.
func TestPathRemove_Zsh_KeepsZshBody(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, ".zshrc", "")

	for _, dir := range []string{"/a", "/b"} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"add", "--file", canonical, dir})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("add %s: %v", dir, err)
		}
	}

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"remove", "--file", canonical, "/a"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("remove: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	if !bytes.Contains(got, []byte("for __dfm_d in /b; do\n")) {
		t.Errorf("expected shrunk zsh loop over /b in:\n%s", got)
	}
	if strings.Contains(string(got), `case ":$PATH:" in`) {
		t.Errorf("posix body written on remove:\n%s", got)
	}
}
