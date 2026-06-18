package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// §7 case #8: shrink list.
func TestPathRemove_ShrinkList(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	for _, d := range []string{"/a", "/b", "/c"} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"add", "--file", canonical, d})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("seed add %s: %v", d, err)
		}
	}

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"remove", "--file", canonical, "/b"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("remove: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1:\n%s", len(entries), got)
	}
	wantID := pathMarkerID(pathDirectionPrepend, []string{"/a", "/c"})
	if entries[0].Marker.ID != wantID {
		t.Errorf("marker id = %q, want %q", entries[0].Marker.ID, wantID)
	}
	if !equalStrings(entries[0].Marker.Dirs, []string{"/a", "/c"}) {
		t.Errorf("dirs = %v, want [/a /c]", entries[0].Marker.Dirs)
	}
	if !bytes.Contains(got, []byte("for __dfm_d in /a /c; do")) {
		t.Errorf("expected for-loop with /a /c in:\n%s", got)
	}

	// Audit: path.remove with entry_deleted=false.
	data, _ := os.ReadFile(logPath)
	logStr := string(data)
	if !strings.Contains(logStr, `"action":"path.remove"`) {
		t.Errorf("missing path.remove event:\n%s", logStr)
	}
	if !strings.Contains(logStr, `"entry_deleted":false`) {
		t.Errorf("missing entry_deleted=false:\n%s", logStr)
	}
}

// §7 case #9: removing the last dir deletes the block entirely.
func TestPathRemove_DeleteBlock(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("seed add: %v", err)
	}

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"remove", "--file", canonical, "/a"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got, _ := os.ReadFile(canonical)
	if bytes.Contains(got, []byte("dfm:path:")) {
		t.Errorf("expected no dfm:path markers after delete, got:\n%s", got)
	}
	entries := findPathManagedEntries(got)
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
	if bytes.Contains(got, []byte("\n\n\n")) {
		t.Errorf("trailing whitespace not cleaned up:\n%q", got)
	}

	data, _ := os.ReadFile(logPath)
	logStr := string(data)
	if !strings.Contains(logStr, `"entry_deleted":true`) {
		t.Errorf("missing entry_deleted=true:\n%s", logStr)
	}
	if !strings.Contains(logStr, `"marker_id_new":""`) {
		t.Errorf("expected marker_id_new=\"\" on block delete:\n%s", logStr)
	}
}

// §7 case #10: removing a non-managed dir exits exitAlreadyOrMiss with
// no mutation, no snapshot, no audit row.
func TestPathRemove_NotFound(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	contentBefore, _ := os.ReadFile(canonical)
	auditBefore, _ := os.ReadFile(logPath)

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"remove", "--file", canonical, "/b"})
	err := cmd2.Execute()
	if err == nil {
		t.Fatalf("expected not-found error, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitAlreadyOrMiss {
		t.Fatalf("want exitError(code=%d), got %v", exitAlreadyOrMiss, err)
	}
	if !strings.Contains(ee.msg, "not managed") {
		t.Errorf("error lacks 'not managed': %q", ee.msg)
	}

	contentAfter, _ := os.ReadFile(canonical)
	if !bytes.Equal(contentBefore, contentAfter) {
		t.Errorf("file mutated on not-found:\nbefore: %s\nafter: %s", contentBefore, contentAfter)
	}
	auditAfter, _ := os.ReadFile(logPath)
	if !bytes.Equal(auditBefore, auditAfter) {
		t.Errorf("audit log mutated on not-found")
	}
}

// Fish target: shrink path on a fish-rendered block. Parallel to §7
// case #8 but exercises the fish renderer round-trip.
func TestPathRemove_Fish_Shrink(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, "config.fish", "")

	for _, d := range []string{"/a", "/b", "/c"} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"add", "--file", canonical, d})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("seed add %s: %v", d, err)
		}
	}

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"remove", "--file", canonical, "/b"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got, _ := os.ReadFile(canonical)
	if !bytes.Contains(got, []byte("for __dfm_d in /a /c\n")) {
		t.Errorf("expected fish for-loop with /a /c in:\n%s", got)
	}
	// Bash-shaped fragment must NOT show up.
	if bytes.Contains(got, []byte(`case ":$PATH:" in`)) {
		t.Errorf("unexpected bash case-guard in fish output:\n%s", got)
	}
}

// Fish target: deleting the last dir excises the whole fish block.
func TestPathRemove_Fish_DeleteBlock(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, "config.fish", "")

	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("seed add: %v", err)
	}

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"remove", "--file", canonical, "/a"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got, _ := os.ReadFile(canonical)
	if bytes.Contains(got, []byte("dfm:path:")) {
		t.Errorf("expected no dfm:path markers after delete in fish file, got:\n%s", got)
	}
}

// Cross-spelling: user adds `/Users/foo/x` and removes `~/x` (with
// HOME=/Users/foo) — must match via normalizePathDir.
func TestPathRemove_CrossSpelling(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	literal := tmp + "/x"
	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, literal})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("seed add: %v", err)
	}

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"remove", "--file", canonical, "~/x"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("remove ~/x: %v", err)
	}

	got, _ := os.ReadFile(canonical)
	if bytes.Contains(got, []byte("dfm:path:")) {
		t.Errorf("expected fixture cleared after cross-spelling remove, got:\n%s", got)
	}
}

// --shell and --file together → exitResolveErr.
func TestPathRemove_ShellAndFileMutuallyExclusive(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"remove", "--shell", "bash", "--file", canonical, "/a"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected mutex error, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	if !strings.Contains(ee.msg, "mutually exclusive") {
		t.Errorf("error lacks 'mutually exclusive': %q", ee.msg)
	}
}

// Empty dir → exitResolveErr.
func TestPathRemove_EmptyDirRejects(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"remove", "--file", canonical, ""})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for empty dir")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
}

// §7 case #17: full audit payload on remove. Spot-check that all
// required fields show up in both the shrink and delete-block paths.
func TestPathRemove_AuditFields(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	for _, d := range []string{"/a", "/b"} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"add", "--file", canonical, d})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("seed add %s: %v", d, err)
		}
	}

	// Shrink case.
	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"remove", "--file", canonical, "/b"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("shrink remove: %v", err)
	}
	data, _ := os.ReadFile(logPath)
	logStr := string(data)
	idAB := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	idA := pathMarkerID(pathDirectionPrepend, []string{"/a"})
	for _, frag := range []string{
		`"action":"path.remove"`,
		`"dir":"/b"`,
		`"direction":"prepend"`,
		`"marker_id_old":"` + idAB + `"`,
		`"marker_id_new":"` + idA + `"`,
		`"entry_deleted":false`,
		`"snapshot_id":`,
	} {
		if !strings.Contains(logStr, frag) {
			t.Errorf("shrink audit missing %q\nlog: %s", frag, logStr)
		}
	}

	// Delete-block case.
	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"remove", "--file", canonical, "/a"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("delete remove: %v", err)
	}
	data, _ = os.ReadFile(logPath)
	logStr = string(data)
	for _, frag := range []string{
		`"dir":"/a"`,
		`"marker_id_old":"` + idA + `"`,
		`"marker_id_new":""`,
		`"entry_deleted":true`,
	} {
		if !strings.Contains(logStr, frag) {
			t.Errorf("delete audit missing %q\nlog: %s", frag, logStr)
		}
	}
}
