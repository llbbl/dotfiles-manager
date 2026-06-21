package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runImportWithStdin executes `dfm path import` with the given args and
// a caller-supplied stdin payload. Splits out from runImport in the
// PR #49 test file so the apply tests can exercise the prompt flow
// without the dry-run shortcut.
func runImportWithStdin(t *testing.T, ctx context.Context, stdin string, args ...string) (string, string, error) {
	t.Helper()
	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"import"}, args...))
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

// importableFixture returns an rc body with three IMPORTABLE prepend
// lines (two bare + one guarded), plus a comment line so the splice
// point and trim logic both get exercised.
func importableFixture() string {
	return `# bashrc
export PATH="/opt/local/bin:$PATH"
export PATH="/usr/local/sbin:$PATH"
case ":$PATH:" in
  *":$HOME/.cargo/bin:"*) ;;
  *) export PATH="$HOME/.cargo/bin:$PATH" ;;
esac
# end
`
}

// --- 1. Happy path apply with --yes --------------------------------

func TestPathImportApply_HappyPathWithYes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, importableFixture())

	stdout, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--yes")
	if err != nil {
		t.Fatalf("execute: %v\nstdout: %s", err, stdout)
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// (a) Originals removed.
	for _, frag := range []string{
		`export PATH="/opt/local/bin:$PATH"`,
		`export PATH="/usr/local/sbin:$PATH"`,
		`*":$HOME/.cargo/bin:"*)`,
	} {
		if bytes.Contains(got, []byte(frag)) {
			t.Errorf("expected original PATH line removed, still present: %q\n%s", frag, got)
		}
	}

	// (b) One dfm:path:<id> block with direction=prepend and 3 dirs in
	// proposal order.
	entries := findPathManagedEntries(got)
	if len(entries) != 1 {
		t.Fatalf("got %d managed entries, want 1:\n%s", len(entries), got)
	}
	e := entries[0]
	if e.Marker.Direction != pathDirectionPrepend {
		t.Errorf("direction = %q, want prepend", e.Marker.Direction)
	}
	wantDirs := []string{"/opt/local/bin", "/usr/local/sbin", "$HOME/.cargo/bin"}
	if !equalStrings(e.Marker.Dirs, wantDirs) {
		t.Errorf("dirs = %v, want %v", e.Marker.Dirs, wantDirs)
	}

	// (c) Snapshot exists on disk via the snapshot manager.
	s, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()
	mgr, mgrErr := newSnapshotManager(ctx, s)
	if mgrErr != nil {
		t.Fatalf("snapshot manager: %v", mgrErr)
	}
	snaps, err := mgr.List(ctx, canonical)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(snaps) == 0 {
		t.Errorf("expected at least one snapshot for %s", canonical)
	}

	// (d) Audit event recorded with the expected sub_action / dirs_count.
	data, _ := os.ReadFile(logPath)
	log := string(data)
	wantFields := []string{
		`"action":"path.import"`,
		`"sub_action":"import"`,
		`"dirs_count":3`,
		`"direction":"prepend"`,
		`"lines_removed":3`,
		`"marker_id_old":""`,
	}
	for _, frag := range wantFields {
		if !strings.Contains(log, frag) {
			t.Errorf("audit log missing %q\nlog: %s", frag, log)
		}
	}

	// Output summary should mention dirs count and marker id.
	if !strings.Contains(stdout, "3 dirs folded into one prepend entry") {
		t.Errorf("stdout missing summary line:\n%s", stdout)
	}
}

// --- 2. Idempotency / re-source ------------------------------------
// Sourcing the resulting rc 3x in bash must NOT duplicate any dir
// in PATH — the dfm-managed block has the same case-guard semantics
// as path add does in path_add_test.go's idempotency check.
func TestPathImportApply_Idempotency(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not on PATH: %v", err)
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ctx, _, _ := setupEditCmdEnv(t)

	// Use absolute paths only — $HOME-style dirs depend on bash
	// expansion semantics, which is incidental to what we're testing.
	rc := `export PATH="/tmp/dfm-import-a:$PATH"
export PATH="/tmp/dfm-import-b:$PATH"
`
	canonical, _ := writeTracked(t, ctx, rc)

	if _, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--yes"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	script := fmt.Sprintf(
		`export PATH=/usr/bin:/bin; source %s; source %s; source %s; echo "$PATH"`,
		canonical, canonical, canonical,
	)
	out, err := exec.Command(bashPath, "-c", script).Output()
	if err != nil {
		t.Fatalf("bash run: %v", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ":")
	for _, dir := range []string{"/tmp/dfm-import-a", "/tmp/dfm-import-b"} {
		count := 0
		for _, p := range parts {
			if p == dir {
				count++
			}
		}
		if count != 1 {
			t.Errorf("dir %q appears %d times in PATH (want 1)\nPATH=%s",
				dir, count, string(out))
		}
	}
}

// --- 3. Decline with "n" → no mutation -----------------------------

func TestPathImportApply_DeclineWithN(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	original := `export PATH="/opt/x:$PATH"
`
	canonical, _ := writeTracked(t, ctx, original)
	auditBefore, _ := os.ReadFile(logPath)

	stdout, _, err := runImportWithStdin(t, ctx, "n\n", "--file", canonical)
	if err != nil {
		t.Fatalf("execute: %v\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "declined") {
		t.Errorf("stdout missing 'declined' notice:\n%s", stdout)
	}
	got, _ := os.ReadFile(canonical)
	if !bytes.Equal(got, []byte(original)) {
		t.Errorf("file mutated on decline:\nbefore: %s\nafter:  %s", original, got)
	}
	auditAfter, _ := os.ReadFile(logPath)
	if !bytes.Equal(auditBefore, auditAfter) {
		t.Errorf("audit log mutated on decline")
	}
}

// --- 4. Empty Enter also declines ----------------------------------

func TestPathImportApply_EmptyEnterDeclines(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	original := `export PATH="/opt/x:$PATH"
`
	canonical, _ := writeTracked(t, ctx, original)

	stdout, _, err := runImportWithStdin(t, ctx, "\n", "--file", canonical)
	if err != nil {
		t.Fatalf("execute: %v\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "declined") {
		t.Errorf("stdout missing 'declined' notice:\n%s", stdout)
	}
	got, _ := os.ReadFile(canonical)
	if !bytes.Equal(got, []byte(original)) {
		t.Errorf("file mutated on empty Enter:\nbefore: %s\nafter:  %s", original, got)
	}
}

// --- 5. y / yes / Y / YES all accept -------------------------------

func TestPathImportApply_YesAcceptances(t *testing.T) {
	for _, answer := range []string{"y\n", "yes\n", "Y\n", "YES\n"} {
		t.Run("answer="+strings.TrimSpace(answer), func(t *testing.T) {
			ctx, _, _ := setupEditCmdEnv(t)
			canonical, _ := writeTracked(t, ctx, `export PATH="/opt/x:$PATH"
`)
			stdout, _, err := runImportWithStdin(t, ctx, answer, "--file", canonical)
			if err != nil {
				t.Fatalf("execute: %v\nstdout: %s", err, stdout)
			}
			if !strings.Contains(stdout, "applied:") {
				t.Errorf("stdout missing 'applied:' for answer %q:\n%s", answer, stdout)
			}
			got, _ := os.ReadFile(canonical)
			entries := findPathManagedEntries(got)
			if len(entries) != 1 {
				t.Errorf("got %d managed entries, want 1 for answer %q:\n%s",
					len(entries), answer, got)
			}
		})
	}
}

// --- 6. --dry-run still works (PR #49 invariant) -------------------

func TestPathImportApply_DryRunUnchanged(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	original := `export PATH="/opt/x:$PATH"
`
	canonical, _ := writeTracked(t, ctx, original)

	stdout, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--dry-run")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "IMPORTABLE") {
		t.Errorf("expected IMPORTABLE section in dry-run:\n%s", stdout)
	}
	if strings.Contains(stdout, "applied:") || strings.Contains(stdout, "declined") {
		t.Errorf("dry-run leaked apply/decline trailer:\n%s", stdout)
	}
	got, _ := os.ReadFile(canonical)
	if !bytes.Equal(got, []byte(original)) {
		t.Errorf("file mutated under --dry-run")
	}
}

// --- 7. --dry-run --yes → dry-run wins -----------------------------

func TestPathImportApply_DryRunBeatsYes(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	original := `export PATH="/opt/x:$PATH"
`
	canonical, _ := writeTracked(t, ctx, original)

	stdout, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--dry-run", "--yes")
	if err != nil {
		t.Fatalf("execute: %v\nstdout: %s", err, stdout)
	}
	got, _ := os.ReadFile(canonical)
	if !bytes.Equal(got, []byte(original)) {
		t.Errorf("file mutated despite --dry-run:\nbefore: %s\nafter:  %s",
			original, got)
	}
	if strings.Contains(stdout, "applied:") {
		t.Errorf("dry-run --yes still printed applied trailer:\n%s", stdout)
	}
}

// --- 8. Refuse when prepend entry already exists -------------------

func TestPathImportApply_RefusesWhenPrependEntryExists(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	managed := wellFormedPathBlock(
		pathDirectionPrepend, []string{"/already"},
		"for __dfm_d in /already; do : ; done\nunset __dfm_d\nexport PATH\n",
	)
	original := "# bashrc\n" + managed + `export PATH="/opt/x:$PATH"
`
	canonical, _ := writeTracked(t, ctx, original)

	_, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--yes")
	if err == nil {
		t.Fatalf("expected refusal, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	if !strings.Contains(ee.msg, "dfm path add") {
		t.Errorf("error should suggest 'dfm path add': %q", ee.msg)
	}

	got, _ := os.ReadFile(canonical)
	if !bytes.Equal(got, []byte(original)) {
		t.Errorf("file mutated on refusal:\nbefore: %s\nafter:  %s", original, got)
	}
}

// --- 9. Empty IMPORTABLE — no prompt, no snapshot ------------------

func TestPathImportApply_EmptyImportableNoPrompt(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	managed := wellFormedPathBlock(
		pathDirectionAppend, []string{"/managed"},
		"for __dfm_d in /managed; do : ; done\nunset __dfm_d\nexport PATH\n",
	)
	// Only managed + dynamic + unknown — no IMPORTABLE.
	original := "# bashrc\n" + managed + `eval "$(mise activate zsh)"
PATH=$(some-script):$PATH
`
	canonical, _ := writeTracked(t, ctx, original)
	auditBefore, _ := os.ReadFile(logPath)

	stdout, _, err := runImportWithStdin(t, ctx, "", "--file", canonical)
	if err != nil {
		t.Fatalf("execute: %v\nstdout: %s", err, stdout)
	}
	// no prompt should appear.
	if strings.Contains(stdout, "Apply?") {
		t.Errorf("unexpected prompt in stdout when nothing is importable:\n%s", stdout)
	}
	got, _ := os.ReadFile(canonical)
	if !bytes.Equal(got, []byte(original)) {
		t.Errorf("file mutated when there was nothing to import")
	}
	auditAfter, _ := os.ReadFile(logPath)
	if !bytes.Equal(auditBefore, auditAfter) {
		t.Errorf("audit log mutated when there was nothing to import")
	}
}

// --- 10. JSON apply trailer -----------------------------------------

func TestPathImportApply_JSONApplyTrailer(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `export PATH="/opt/x:$PATH"
export PATH="/opt/y:$PATH"
`)

	stdout, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--yes", "--json")
	if err != nil {
		t.Fatalf("execute: %v\nstdout: %s", err, stdout)
	}
	trailer := lastJSONLine(t, stdout)
	var res pathImportApplyResult
	if err := json.Unmarshal([]byte(trailer), &res); err != nil {
		t.Fatalf("unmarshal trailer %q: %v", trailer, err)
	}
	if !res.Applied {
		t.Errorf("expected applied=true, got %+v", res)
	}
	if res.SnapshotID == "" {
		t.Errorf("expected snapshot_id, got %+v", res)
	}
	if res.DirsCount != 2 {
		t.Errorf("dirs_count = %d, want 2", res.DirsCount)
	}
	wantID := pathMarkerID(pathDirectionPrepend, []string{"/opt/x", "/opt/y"})
	if res.MarkerID != wantID {
		t.Errorf("marker_id = %q, want %q", res.MarkerID, wantID)
	}
}

// --- 11. JSON decline trailer --------------------------------------

func TestPathImportApply_JSONDeclineTrailer(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `export PATH="/opt/x:$PATH"
`)
	stdout, _, err := runImportWithStdin(t, ctx, "n\n", "--file", canonical, "--json")
	if err != nil {
		t.Fatalf("execute: %v\nstdout: %s", err, stdout)
	}
	trailer := lastJSONLine(t, stdout)
	var res pathImportApplyResult
	if err := json.Unmarshal([]byte(trailer), &res); err != nil {
		t.Fatalf("unmarshal trailer %q: %v", trailer, err)
	}
	if res.Applied {
		t.Errorf("expected applied=false on decline, got %+v", res)
	}
	if res.Reason != "user-declined" {
		t.Errorf("reason = %q, want user-declined", res.Reason)
	}
}

// --- 12. Audit payload shape ---------------------------------------

func TestPathImportApply_AuditPayloadShape(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `export PATH="/opt/x:$PATH"
export PATH="/opt/y:$PATH"
`)

	if _, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--yes"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(data)
	wantID := pathMarkerID(pathDirectionPrepend, []string{"/opt/x", "/opt/y"})
	wantFields := []string{
		`"action":"path.import"`,
		`"sub_action":"import"`,
		`"marker_id_new":"` + wantID + `"`,
		`"marker_id_old":""`,
		`"direction":"prepend"`,
		`"dirs_count":2`,
		`"lines_removed":2`,
		`"snapshot_id":`,
	}
	for _, frag := range wantFields {
		if !strings.Contains(log, frag) {
			t.Errorf("audit log missing %q\nlog: %s", frag, log)
		}
	}
}

// --- 13. Append-direction guard (forward-compat) -------------------
// An existing managed append entry must be left byte-identical and the
// import must proceed normally.
func TestPathImportApply_AppendEntryUntouched(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	appendBlock := wellFormedPathBlock(
		pathDirectionAppend, []string{"/managed-append"},
		"for __dfm_d in /managed-append; do : ; done\nunset __dfm_d\nexport PATH\n",
	)
	original := `# bashrc
export PATH="/opt/x:$PATH"
` + appendBlock
	canonical, _ := writeTracked(t, ctx, original)

	if _, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--yes"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got, _ := os.ReadFile(canonical)
	if !bytes.Contains(got, []byte(appendBlock)) {
		t.Errorf("append block was mutated\nbefore block: %s\nafter file:   %s",
			appendBlock, got)
	}
	// And the import succeeded — exactly two managed entries now.
	entries := findPathManagedEntries(got)
	if len(entries) != 2 {
		t.Fatalf("expected 2 managed entries (existing append + new prepend), got %d:\n%s",
			len(entries), got)
	}
}

// --- 14. Snapshot round-trip (blob exists with pre-edit bytes) -----

func TestPathImportApply_SnapshotPreservesPreEditBytes(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	original := importableFixture()
	canonical, _ := writeTracked(t, ctx, original)

	if _, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--yes"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	s, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()
	mgr, mgrErr := newSnapshotManager(ctx, s)
	if mgrErr != nil {
		t.Fatalf("snapshot manager: %v", mgrErr)
	}
	snaps, err := mgr.List(ctx, canonical)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) == 0 {
		t.Fatalf("expected a pre-edit snapshot")
	}
	// Pick the most recent pre-edit snapshot.
	var preEdit *string
	for i := range snaps {
		if string(snaps[i].Reason) == "pre-edit" {
			path := snaps[i].StoragePath
			preEdit = &path
			break
		}
	}
	if preEdit == nil {
		t.Fatalf("no pre-edit snapshot found in %d snapshots", len(snaps))
	}
	gotBytes, err := os.ReadFile(*preEdit)
	if err != nil {
		t.Fatalf("read snapshot blob %s: %v", *preEdit, err)
	}
	if string(gotBytes) != original {
		t.Errorf("snapshot blob != pre-edit bytes\nwant:\n%s\ngot:\n%s",
			original, string(gotBytes))
	}
}

// --- 15. Final-newline preserved (with and without trailing \n) ----

func TestPathImportApply_FinalNewlinePreservation(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"with-trailing-newline", `export PATH="/opt/x:$PATH"
`},
		{"without-trailing-newline", `export PATH="/opt/x:$PATH"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, _ := setupEditCmdEnv(t)
			canonical, _ := writeTracked(t, ctx, tc.content)

			if _, _, err := runImportWithStdin(t, ctx, "", "--file", canonical, "--yes"); err != nil {
				t.Fatalf("execute: %v", err)
			}
			got, _ := os.ReadFile(canonical)
			hadTrailing := strings.HasSuffix(tc.content, "\n")
			gotTrailing := bytes.HasSuffix(got, []byte("\n"))
			if hadTrailing != gotTrailing {
				t.Errorf("final-newline state changed: before=%v after=%v\nresult: %q",
					hadTrailing, gotTrailing, got)
			}
		})
	}
}

// --- helpers --------------------------------------------------------

// lastJSONLine extracts the final standalone JSON object on stdout.
// The PR2 apply contract emits the indented proposal document followed
// by a compact single-line trailer like `{"applied":...}`. We walk
// stdout from the bottom and return the first line that successfully
// parses as a JSON object — that filters out the indented proposal's
// closing `}` line which contains only the brace.
func lastJSONLine(t *testing.T, stdout string) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(ln), &v); err == nil {
			return ln
		}
	}
	t.Fatalf("no parseable JSON object line in stdout:\n%s", stdout)
	return ""
}

