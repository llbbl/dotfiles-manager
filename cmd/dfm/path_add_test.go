package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// writeTrackedNamed is a sibling of writeTracked that lets the test
// specify the basename of the fixture file. Needed for the fish-file
// guard test, which relies on resolveAliasTarget inferring the family
// from the .fish suffix.
func writeTrackedNamed(t *testing.T, ctx context.Context, basename, contents string) (canonical, display string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), basename)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, d, err := tracker.Resolve(path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	s, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()
	if _, err := tracker.Track(ctx, s, c, d, tracker.TrackOptions{SkipSecretCheck: true}); err != nil {
		t.Fatalf("track: %v", err)
	}
	return c, d
}

// First add creates entry: §7 case #1.
func TestPathAdd_FirstAddCreatesEntry(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "# bashrc\n")

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
	// Body shape sanity: for-loop iterates `/a`, case-guard, prepend assignment.
	expectFragments := []string{
		"for __dfm_d in /a; do",
		`*":$__dfm_d:"*) ;;`,
		`*) PATH="$__dfm_d:$PATH" ;;`,
		"unset __dfm_d",
		"export PATH",
	}
	for _, frag := range expectFragments {
		if !bytes.Contains(got, []byte(frag)) {
			t.Errorf("expected fragment %q in:\n%s", frag, got)
		}
	}
	// Exactly one managed entry.
	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Marker.ID != wantID {
		t.Errorf("marker id = %q, want %q", entries[0].Marker.ID, wantID)
	}

	// Audit event recorded.
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"action":"path.add"`) {
		t.Errorf("missing path.add event:\n%s", data)
	}
	if !strings.Contains(string(data), `"direction":"prepend"`) {
		t.Errorf("missing direction=prepend audit field:\n%s", data)
	}
}

// Second add mutates in place: §7 case #2.
func TestPathAdd_SecondAddMutatesInPlace(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "# bashrc\n")

	// First add: /a.
	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("first add: %v", err)
	}
	oldID := pathMarkerID(pathDirectionPrepend, []string{"/a"})

	// Second add: /b.
	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"add", "--file", canonical, "/b"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("second add: %v", err)
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Old marker id is gone.
	if bytes.Contains(got, []byte("dfm:path:"+oldID)) {
		t.Errorf("old marker id %q still present:\n%s", oldID, got)
	}

	// Exactly one managed entry.
	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1:\n%s", len(entries), got)
	}
	newID := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	if entries[0].Marker.ID != newID {
		t.Errorf("new marker id = %q, want %q", entries[0].Marker.ID, newID)
	}
	if !equalStrings(entries[0].Marker.Dirs, []string{"/a", "/b"}) {
		t.Errorf("dirs = %v, want [/a /b]", entries[0].Marker.Dirs)
	}
	// for-loop body iterates in order.
	if !bytes.Contains(got, []byte("for __dfm_d in /a /b; do")) {
		t.Errorf("expected for-loop ordering /a /b in:\n%s", got)
	}
}

// Idempotency on re-source: §7 case #3 — THE load-bearing test.
func TestPathAdd_IdempotencyOnResource(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not on PATH: %v", err)
	}

	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	for _, dir := range []string{"/tmp/dfm-test-a", "/tmp/dfm-test-b"} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"add", "--file", canonical, dir})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("add %s: %v", dir, err)
		}
	}

	// Source the file 3x in bash with a clean PATH, then count
	// occurrences of each managed dir.
	script := fmt.Sprintf(
		`export PATH=/usr/bin:/bin; source %s; source %s; source %s; echo "$PATH"`,
		canonical, canonical, canonical,
	)
	out, err := exec.Command(bashPath, "-c", script).Output()
	if err != nil {
		t.Fatalf("bash run: %v", err)
	}
	pathLine := strings.TrimSpace(string(out))
	parts := strings.Split(pathLine, ":")

	for _, dir := range []string{"/tmp/dfm-test-a", "/tmp/dfm-test-b"} {
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
}

// --append creates a separate managed entry: §7 case #4.
func TestPathAdd_AppendCreatesSeparateEntry(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "# bashrc\n")

	for _, args := range [][]string{
		{"add", "--file", canonical, "/a"},
		{"add", "--file", canonical, "--append", "/b"},
	} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute %v: %v", args, err)
		}
	}

	got, _ := os.ReadFile(canonical)
	entries := findPathManagedEntries(got)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2:\n%s", len(entries), got)
	}
	directions := map[string]bool{}
	for _, e := range entries {
		directions[e.Marker.Direction] = true
	}
	if !directions[pathDirectionPrepend] || !directions[pathDirectionAppend] {
		t.Errorf("expected both prepend and append entries, got: %v", directions)
	}
	if entries[0].Marker.ID == entries[1].Marker.ID {
		t.Errorf("expected distinct marker ids, both = %q", entries[0].Marker.ID)
	}
	// The append entry's body must use the append-direction assignment.
	if !bytes.Contains(got, []byte(`*) PATH="$PATH:$__dfm_d" ;;`)) {
		t.Errorf("missing append-direction assignment in:\n%s", got)
	}
}

// Dedup refuses same-direction duplicate: §7 case #5.
func TestPathAdd_DedupRefuses(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("first add: %v", err)
	}
	contentAfterFirst, _ := os.ReadFile(canonical)
	auditAfterFirst, _ := os.ReadFile(logPath)

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"add", "--file", canonical, "/a"})
	err := cmd2.Execute()
	if err == nil {
		t.Fatalf("expected dedup error, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitAlreadyOrMiss {
		t.Fatalf("want exitError(code=%d), got %v", exitAlreadyOrMiss, err)
	}

	contentAfterSecond, _ := os.ReadFile(canonical)
	if !bytes.Equal(contentAfterFirst, contentAfterSecond) {
		t.Errorf("file mutated on dedup refusal:\nbefore: %s\nafter:  %s",
			contentAfterFirst, contentAfterSecond)
	}
	auditAfterSecond, _ := os.ReadFile(logPath)
	if !bytes.Equal(auditAfterFirst, auditAfterSecond) {
		t.Errorf("audit log mutated on dedup refusal")
	}
}

// Cross-spelling dedup: §7 case #6. `$HOME/x` then `~/x`.
func TestPathAdd_CrossSpellingDedup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "$HOME/x"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("first add: %v", err)
	}

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"add", "--file", canonical, "~/x"})
	err := cmd2.Execute()
	if err == nil {
		t.Fatalf("expected cross-spelling dedup error, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitAlreadyOrMiss {
		t.Fatalf("want exitError(code=%d), got %v", exitAlreadyOrMiss, err)
	}
}

// --force bypasses secrets but NOT dedup: §7 case #7 (secret half).
// Companion to TestPathAdd_ForceDoesNotBypassDedup; together the two
// pin both halves of the --force contract: secrets yes, dedup no.
//
// The path-add scanner runs over the dir token itself (see path_add.go
// near `secrets.ScanReader(bytes.NewReader([]byte(dir)))`), so the
// fixture is a dir whose name embeds a value the AWS-access-key rule
// recognizes. Without --force the command exits exitSecretsErr and
// leaves the rc file byte-identical; with --force it proceeds and the
// managed block is written.
func TestPathAdd_ForceBypassesSecretScan(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	// AWS access key id pattern \bAKIA[0-9A-Z]{16}\b — embedded inside
	// a dir token so the secret rule fires when the scanner walks
	// `dir`. The token itself is the canonical AWS docs example value,
	// so even if the dir doesn't exist on disk the regex is the only
	// thing that matters here.
	secretDir := "/opt/AKIAIOSFODNN7EXAMPLE/bin"

	before, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	// Without --force: must exit exitSecretsErr, file untouched.
	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	var stderr1 bytes.Buffer
	cmd1.SetErr(&stderr1)
	cmd1.SetArgs([]string{"add", "--file", canonical, secretDir})
	err = cmd1.Execute()
	if err == nil {
		t.Fatalf("expected secrets error, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitSecretsErr {
		t.Fatalf("want exitError(code=%d), got %v", exitSecretsErr, err)
	}
	after, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("file mutated on secrets refusal:\nbefore: %s\nafter:  %s", before, after)
	}

	// With --force: succeeds. Managed block is written, dir token
	// round-trips into the marker `dirs=` field.
	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"add", "--file", canonical, "--force", secretDir})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("force add: %v", err)
	}
	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read forced: %v", err)
	}
	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1:\n%s", len(entries), got)
	}
	if !equalStrings(entries[0].Marker.Dirs, []string{secretDir}) {
		t.Errorf("dirs = %v, want [%s]", entries[0].Marker.Dirs, secretDir)
	}
}

// --force does NOT bypass dedup: §7 case #7 (dedup half — the secret
// scanner half is covered by TestPathAdd_ForceBypassesSecretScan).
func TestPathAdd_ForceDoesNotBypassDedup(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("first add: %v", err)
	}

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"add", "--file", canonical, "--force", "/a"})
	err := cmd2.Execute()
	if err == nil {
		t.Fatalf("expected dedup error even with --force, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitAlreadyOrMiss {
		t.Fatalf("want exitError(code=%d), got %v", exitAlreadyOrMiss, err)
	}
}

// Empty dir: §7 case #14.
func TestPathAdd_EmptyDirRejects(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "--file", canonical, ""})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for empty dir")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
}

// --shell + --file together: §7 case #15.
func TestPathAdd_ShellAndFileMutuallyExclusive(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "--shell", "bash", "--file", canonical, "/a"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected mutex error, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	if !strings.Contains(ee.msg, "mutually exclusive") {
		t.Errorf("error message lacks 'mutually exclusive': %q", ee.msg)
	}
}

// Audit fields on add: §7 case #16. Verify dir / direction /
// marker_id_new / marker_id_old / snapshot_id / sub_action.
func TestPathAdd_AuditFields(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	// First add: marker_id_old should be empty.
	cmd1 := newPathCmd()
	cmd1.SetContext(ctx)
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	cmd1.SetArgs([]string{"add", "--file", canonical, "/a"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("first add: %v", err)
	}

	data, _ := os.ReadFile(logPath)
	first := string(data)
	idA := pathMarkerID(pathDirectionPrepend, []string{"/a"})
	wantFirst := []string{
		`"action":"path.add"`,
		`"dir":"/a"`,
		`"direction":"prepend"`,
		`"marker_id_new":"` + idA + `"`,
		`"marker_id_old":""`,
		`"sub_action":"add"`,
		`"snapshot_id":`,
	}
	for _, frag := range wantFirst {
		if !strings.Contains(first, frag) {
			t.Errorf("first-add audit missing %q\nlog: %s", frag, first)
		}
	}

	// Second add: marker_id_old should now be the previous id.
	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"add", "--file", canonical, "/b"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("second add: %v", err)
	}
	data, _ = os.ReadFile(logPath)
	second := string(data)
	idAB := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	wantSecond := []string{
		`"dir":"/b"`,
		`"marker_id_new":"` + idAB + `"`,
		`"marker_id_old":"` + idA + `"`,
	}
	for _, frag := range wantSecond {
		if !strings.Contains(second, frag) {
			t.Errorf("second-add audit missing %q\nlog: %s", frag, second)
		}
	}
}

// Coexistence with pnpm: §7 case #18. pnpm's case-block must be
// byte-identical before/after a path add operation.
func TestPathAdd_CoexistsWithPnpm(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	pnpmBlock := `# pnpm
export PNPM_HOME="$HOME/Library/pnpm"
case ":$PATH:" in
  *":$PNPM_HOME:"*) ;;
  *) export PATH="$PNPM_HOME:$PATH" ;;
esac
# pnpm end
`
	canonical, _ := writeTracked(t, ctx, pnpmBlock)

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "--file", canonical, "/opt/bin"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, _ := os.ReadFile(canonical)
	if !bytes.Contains(got, []byte(pnpmBlock)) {
		t.Errorf("pnpm block was mutated:\nbefore: %s\nafter:  %s", pnpmBlock, got)
	}
	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("expected 1 dfm-managed entry, got %d", len(entries))
	}
}

// Corruption guard: §7 case #19. Two managed entries with the same
// direction must trigger a hard error.
func TestPathAdd_CorruptionGuardMultipleSameDirection(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)

	// Hand-craft two prepend entries with different ids by varying
	// dirs. Use formatPathOpenMarker / formatPathCloseMarker so the
	// fixture is guaranteed to parse.
	id1 := pathMarkerID(pathDirectionPrepend, []string{"/a"})
	id2 := pathMarkerID(pathDirectionPrepend, []string{"/b"})
	open1 := "# dfm:path:" + id1 + " >>>  updated=2026-01-01T00:00:00Z  direction=prepend  dirs=/a\n"
	close1 := "# dfm:path:" + id1 + " <<<\n"
	open2 := "# dfm:path:" + id2 + " >>>  updated=2026-01-01T00:00:00Z  direction=prepend  dirs=/b\n"
	close2 := "# dfm:path:" + id2 + " <<<\n"
	fixture := open1 + "body1\n" + close1 + "\n" + open2 + "body2\n" + close2

	canonical, _ := writeTracked(t, ctx, fixture)

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "--file", canonical, "/c"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected corruption-guard error, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	if !strings.Contains(ee.msg, "multiple") || !strings.Contains(ee.msg, "prepend") {
		t.Errorf("error message doesn't mention 'multiple prepend': %q", ee.msg)
	}
}

// Fish targets are first-class as of dfm-mxf. Positive fish-flow
// coverage now lives in path_add_fish_test.go; this stub is left as a
// breadcrumb so future grep'rs find their way.
