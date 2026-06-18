package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// First add into a fresh fish target creates a managed entry with the
// fish-specific block body per §5.3.
func TestPathAdd_Fish_FirstAddCreatesEntry(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, "config.fish", "# fish config\n")

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

	wantID := pathMarkerID(pathDirectionPrepend, []string{"/a"})
	openTok := "# dfm:path:" + wantID + " >>>"
	closeTok := "# dfm:path:" + wantID + " <<<"
	if !bytes.Contains(got, []byte(openTok)) {
		t.Errorf("missing open marker %q in:\n%s", openTok, got)
	}
	if !bytes.Contains(got, []byte(closeTok)) {
		t.Errorf("missing close marker %q in:\n%s", closeTok, got)
	}
	// Body shape sanity: fish for-loop, contains-guard, set -gx with
	// prepend direction, trailing `set -e`. Spec §5.3 fragments.
	expectFragments := []string{
		"for __dfm_d in /a\n",
		"    if not contains -- $__dfm_d $PATH\n",
		"        set -gx PATH $__dfm_d $PATH\n",
		"    end\n",
		"end\n",
		"set -e __dfm_d\n",
	}
	for _, frag := range expectFragments {
		if !bytes.Contains(got, []byte(frag)) {
			t.Errorf("expected fragment %q in:\n%s", frag, got)
		}
	}
	// Must NOT carry bash/zsh-only fragments.
	for _, frag := range []string{`case ":$PATH:" in`, "unset __dfm_d", "export PATH\n"} {
		if bytes.Contains(got, []byte(frag)) {
			t.Errorf("unexpected bash/zsh fragment %q in fish output:\n%s", frag, got)
		}
	}

	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Marker.ID != wantID {
		t.Errorf("marker id = %q, want %q", entries[0].Marker.ID, wantID)
	}
}

// Second add into a fish target mutates the same block in place and
// rotates the marker id.
func TestPathAdd_Fish_SecondAddMutatesInPlace(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, "config.fish", "# fish config\n")

	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("first add: %v", err)
	}
	oldID := pathMarkerID(pathDirectionPrepend, []string{"/a"})

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"add", "--file", canonical, "/b"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("second add: %v", err)
	}

	got, _ := os.ReadFile(canonical)
	if bytes.Contains(got, []byte("dfm:path:"+oldID)) {
		t.Errorf("old marker id %q still present:\n%s", oldID, got)
	}
	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1:\n%s", len(entries), got)
	}
	newID := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	if entries[0].Marker.ID != newID {
		t.Errorf("new marker id = %q, want %q", entries[0].Marker.ID, newID)
	}
	if !bytes.Contains(got, []byte("for __dfm_d in /a /b\n")) {
		t.Errorf("expected fish for-loop iterating /a /b in:\n%s", got)
	}
}

// --append flips the fish set line direction to `$PATH $__dfm_d`.
func TestPathAdd_Fish_AppendDirection(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, "config.fish", "")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "--file", canonical, "--append", "/a"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, _ := os.ReadFile(canonical)
	if !bytes.Contains(got, []byte("        set -gx PATH $PATH $__dfm_d\n")) {
		t.Errorf("missing append-direction fish set line in:\n%s", got)
	}
	if bytes.Contains(got, []byte("set -gx PATH $__dfm_d $PATH")) {
		t.Errorf("unexpected prepend-direction set line in append block:\n%s", got)
	}
}

// Dedup refuses same-direction duplicate on fish targets just like
// bash/zsh — the dedup machinery is shell-agnostic.
func TestPathAdd_Fish_DedupRefuses(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, "config.fish", "")

	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("first add: %v", err)
	}
	before, _ := os.ReadFile(canonical)

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd2.Execute(); err == nil {
		t.Fatalf("expected dedup error, got nil")
	}
	after, _ := os.ReadFile(canonical)
	if !bytes.Equal(before, after) {
		t.Errorf("file mutated on dedup refusal:\nbefore: %s\nafter:  %s", before, after)
	}
}

// Idempotency on re-source: §7 case #3 mirror for fish — sourcing the
// generated fish snippet 3x must leave each managed dir in $PATH
// exactly once. THE load-bearing acceptance check for the fish path.
func TestPathAdd_Fish_IdempotencyOnResource(t *testing.T) {
	fishPath, err := exec.LookPath("fish")
	if err != nil {
		t.Skipf("fish not on PATH: %v", err)
	}

	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, "config.fish", "")

	for _, dir := range []string{"/tmp/dfm-fish-a", "/tmp/dfm-fish-b"} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"add", "--file", canonical, dir})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("add %s: %v", dir, err)
		}
	}

	// Source the file 3x in fish with a clean PATH, then count
	// occurrences of each managed dir. Fish's "set -gx PATH ..." takes
	// space-separated args, so `echo $PATH` joins entries with spaces.
	script := fmt.Sprintf(
		`set -gx PATH /usr/bin /bin; source %s; source %s; source %s; echo $PATH`,
		canonical, canonical, canonical,
	)
	out, err := exec.Command(fishPath, "-c", script).Output()
	if err != nil {
		t.Fatalf("fish run: %v", err)
	}
	pathLine := strings.TrimSpace(string(out))
	parts := strings.Fields(pathLine)

	for _, dir := range []string{"/tmp/dfm-fish-a", "/tmp/dfm-fish-b"} {
		count := 0
		for _, p := range parts {
			if p == dir {
				count++
			}
		}
		if count != 1 {
			t.Errorf("dir %q appears %d times in PATH (want 1)\nPATH=%s", dir, count, pathLine)
		}
	}
	t.Logf("fish idempotency test ran with fish=%s", fishPath)
}
