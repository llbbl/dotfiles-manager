package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runImport executes `dfm path import` against the given canonical path
// on the supplied context. Returns stdout, stderr, and the Cobra error.
// Extra flags are appended verbatim — callers control --dry-run, --json,
// --shell, etc.
func runImport(t *testing.T, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"import"}, args...))
	// IMPORTANT: capture buffer contents AFTER Execute returns. Returning
	// `stdout.String(), …, cmd.Execute()` would snapshot stdout BEFORE
	// the call lands (Go evaluates return expressions left-to-right).
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

// --- 1. Bare prepend — single line, quoted absolute path -----------

func TestPathImport_BareQuotedAbsolute(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `# bashrc
export PATH="/opt/local/bin:$PATH"
`)
	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "IMPORTABLE") {
		t.Errorf("missing IMPORTABLE section:\n%s", stdout)
	}
	if !strings.Contains(stdout, "/opt/local/bin") {
		t.Errorf("missing /opt/local/bin in:\n%s", stdout)
	}
	if !strings.Contains(stdout, "fold 1 dirs into one prepend entry") {
		t.Errorf("expected proposal line for 1 dir in:\n%s", stdout)
	}
}

// --- 2. Bare prepend — $HOME and ~ variants -------------------------

func TestPathImport_BareHomeVariants(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `export PATH="$HOME/go/bin:$PATH"
export PATH="${HOME}/foo:$PATH"
export PATH="~/cargo/bin:$PATH"
export PATH=$HOME/extra:$PATH
`)
	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.Importable) != 4 {
		t.Fatalf("got %d importable, want 4:\n%s", len(report.Importable), stdout)
	}
	wantDirs := []string{"$HOME/go/bin", "${HOME}/foo", "~/cargo/bin", "$HOME/extra"}
	for i, want := range wantDirs {
		if report.Importable[i].Dir != want {
			t.Errorf("importable[%d].dir = %q, want %q", i, report.Importable[i].Dir, want)
		}
	}
}

// --- 3. Guarded prepend (pnpm-style) — happy path -------------------

func TestPathImport_GuardedHappy(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `# pnpm
case ":$PATH:" in
  *":$HOME/.cargo/bin:"*) ;;
  *) export PATH="$HOME/.cargo/bin:$PATH" ;;
esac
# end
`)
	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.Importable) != 1 {
		t.Fatalf("got %d importable, want 1:\n%s", len(report.Importable), stdout)
	}
	got := report.Importable[0]
	if got.Shape != pathImportShapeGuarded {
		t.Errorf("shape = %q, want guarded", got.Shape)
	}
	if got.Dir != "$HOME/.cargo/bin" {
		t.Errorf("dir = %q, want $HOME/.cargo/bin", got.Dir)
	}
	if !strings.Contains(got.Raw, "case \":$PATH:\"") || !strings.Contains(got.Raw, "esac") {
		t.Errorf("raw should be full block, got: %q", got.Raw)
	}
	// line should be the opening `case` keyword's 1-based index.
	if got.Line != 2 {
		t.Errorf("line = %d, want 2 (the `case` keyword line)", got.Line)
	}
}

// --- 4. Guarded prepend with mismatched literals → UNKNOWN ----------

func TestPathImport_GuardedMismatchFallsToUnknown(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `case ":$PATH:" in
  *":/opt/A:"*) ;;
  *) export PATH="/opt/B:$PATH" ;;
esac
`)
	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.Importable) != 0 {
		t.Errorf("expected 0 importable on literal mismatch, got %d", len(report.Importable))
	}
	// The `case` head should appear in UNKNOWN with the mismatch hint.
	if len(report.SkippedUnknown) == 0 {
		t.Fatalf("expected UNKNOWN entries, got 0\n%s", stdout)
	}
	if !strings.Contains(report.SkippedUnknown[0].Reason, "guarded") {
		t.Errorf("unknown reason = %q, want guarded-mismatch hint",
			report.SkippedUnknown[0].Reason)
	}
}

// --- 5. Dynamic skips: eval, source, plugin loader ------------------

func TestPathImport_DynamicSkips(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `eval "$(mise activate zsh)"
source ~/.nvm/nvm.sh
source ~/.oh-my-zsh.sh
`)
	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.SkippedDynamic) != 3 {
		t.Fatalf("got %d dynamic skips, want 3:\n%s", len(report.SkippedDynamic), stdout)
	}
	wantReasons := []string{"eval-activate", "source", "plugin-loader"}
	for i, want := range wantReasons {
		if report.SkippedDynamic[i].Reason != want {
			t.Errorf("skipped_dynamic[%d].reason = %q, want %q",
				i, report.SkippedDynamic[i].Reason, want)
		}
	}
}

// --- 6. Already-managed skip — one entry per block, not per line ----

func TestPathImport_ManagedBlockSkippedAsOne(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	dirs := []string{"/a", "/b", "/c"}
	managedBlock := wellFormedPathBlock(
		pathDirectionPrepend, dirs,
		"for __dfm_d in /a /b /c; do\n  case \":$PATH:\" in\n    *\":$__dfm_d:\"*) ;;\n    *) PATH=\"$__dfm_d:$PATH\" ;;\n  esac\ndone\nunset __dfm_d\nexport PATH\n",
	)
	canonical, _ := writeTracked(t, ctx, "# bashrc\n"+managedBlock)

	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.SkippedManaged) != 1 {
		t.Fatalf("got %d managed skips, want 1 (one per block):\n%s",
			len(report.SkippedManaged), stdout)
	}
	got := report.SkippedManaged[0]
	if got.DirCount != 3 {
		t.Errorf("dir_count = %d, want 3", got.DirCount)
	}
	if got.Direction != pathDirectionPrepend {
		t.Errorf("direction = %q, want prepend", got.Direction)
	}
	if got.MarkerID == "" {
		t.Errorf("expected non-empty marker_id, got empty")
	}
	if len(report.Importable) != 0 {
		t.Errorf("expected 0 importable inside managed block, got %d", len(report.Importable))
	}
	if len(report.SkippedUnknown) != 0 {
		t.Errorf("expected 0 unknown inside managed block, got %d", len(report.SkippedUnknown))
	}
}

// --- 7. Unknown: command substitution and unresolved expansion ------

func TestPathImport_UnknownShapes(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `PATH=$(some-script):$PATH
export PATH="${FOO_DIR}:$PATH"
`)
	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.SkippedUnknown) != 2 {
		t.Fatalf("got %d unknown, want 2:\n%s", len(report.SkippedUnknown), stdout)
	}
	reasons := []string{report.SkippedUnknown[0].Reason, report.SkippedUnknown[1].Reason}
	wantReasons := []string{"command-substitution", "parameter-expansion"}
	for i, want := range wantReasons {
		if reasons[i] != want {
			t.Errorf("unknown[%d].reason = %q, want %q", i, reasons[i], want)
		}
	}
}

// --- 8. Duplicate IMPORTABLE → first wins in proposal ---------------

func TestPathImport_DuplicateImportable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `export PATH="$HOME/x:$PATH"
export PATH="~/x:$PATH"
`)
	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.Importable) != 2 {
		t.Fatalf("expected both importable rows, got %d", len(report.Importable))
	}
	if report.Proposal == nil || len(report.Proposal.Dirs) != 1 {
		t.Fatalf("expected proposal with 1 dir (first wins), got %#v", report.Proposal)
	}
	if report.Proposal.Dirs[0] != "$HOME/x" {
		t.Errorf("proposal dir = %q, want $HOME/x", report.Proposal.Dirs[0])
	}
	// Human output should call out the duplicate.
	human, _, err := runImport(t, ctx, "--file", canonical, "--dry-run")
	if err != nil {
		t.Fatalf("execute (human): %v", err)
	}
	if !strings.Contains(human, "duplicate of line") {
		t.Errorf("human output missing 'duplicate of line' note:\n%s", human)
	}
}

// --- 9. Mixed rc exercises every bucket — human + JSON consistency --

func TestPathImport_MixedRcAllBuckets(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// One of each bucket: bare, guarded, eval, source, plugin-loader,
	// unknown (command-substitution), plus a managed block.
	managed := wellFormedPathBlock(
		pathDirectionAppend, []string{"/managed"},
		"for __dfm_d in /managed; do : ; done\nunset __dfm_d\nexport PATH\n",
	)
	rc := `# bashrc
export PATH="$HOME/go/bin:$PATH"
case ":$PATH:" in
  *":/opt/x:"*) ;;
  *) export PATH="/opt/x:$PATH" ;;
esac
eval "$(mise activate zsh)"
source ~/.nvm/nvm.sh
source ~/.oh-my-zsh.sh
PATH=$(some-script):$PATH
` + managed

	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, rc)

	// JSON shape sanity.
	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute json: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.Importable) != 2 {
		t.Errorf("importable count = %d, want 2 (bare + guarded)", len(report.Importable))
	}
	if len(report.SkippedDynamic) != 3 {
		t.Errorf("dynamic count = %d, want 3", len(report.SkippedDynamic))
	}
	if len(report.SkippedManaged) != 1 {
		t.Errorf("managed count = %d, want 1", len(report.SkippedManaged))
	}
	if len(report.SkippedUnknown) != 1 {
		t.Errorf("unknown count = %d, want 1", len(report.SkippedUnknown))
	}
	if report.Proposal == nil || len(report.Proposal.Dirs) != 2 {
		t.Fatalf("proposal = %#v, want 2 dirs", report.Proposal)
	}

	// Human output should mention every section header.
	human, _, err := runImport(t, ctx, "--file", canonical, "--dry-run")
	if err != nil {
		t.Fatalf("execute human: %v", err)
	}
	for _, header := range []string{
		"IMPORTABLE",
		"SKIPPED (dynamic",
		"SKIPPED (already dfm-managed)",
		"SKIPPED (unknown shape",
		"proposal:",
	} {
		if !strings.Contains(human, header) {
			t.Errorf("human output missing %q:\n%s", header, human)
		}
	}
}

// --- 10. --shell + --file mutex → exitResolveErr --------------------

func TestPathImport_ShellAndFileMutuallyExclusive(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	_, _, err := runImport(t, ctx, "--shell", "bash", "--file", canonical, "--dry-run")
	if err == nil {
		t.Fatalf("expected mutex error")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	if !strings.Contains(ee.msg, "mutually exclusive") {
		t.Errorf("error message lacks 'mutually exclusive': %q", ee.msg)
	}
}

// --- 11. fish --shell rejected ------------------------------------

func TestPathImport_FishShellRejected(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	_, _, err := runImport(t, ctx, "--shell", "fish", "--dry-run")
	if err == nil {
		t.Fatalf("expected fish-not-supported error")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	if !strings.Contains(ee.msg, "fish") {
		t.Errorf("error message lacks 'fish': %q", ee.msg)
	}
}

// --- 12. Empty rc file → "no importable PATH lines found." ----------

func TestPathImport_EmptyRcEmitsSentinel(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "no importable PATH lines found.") {
		t.Errorf("expected empty-sentinel in:\n%s", stdout)
	}
}

// --- 13. Only-managed rc → no IMPORTABLE, no proposal --------------

func TestPathImport_OnlyManagedNoProposal(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	managed := wellFormedPathBlock(
		pathDirectionPrepend, []string{"/already"},
		"for __dfm_d in /already; do : ; done\nunset __dfm_d\nexport PATH\n",
	)
	canonical, _ := writeTracked(t, ctx, "# bashrc\n"+managed)

	stdout, _, err := runImport(t, ctx, "--file", canonical, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("execute json: %v", err)
	}
	var report pathImportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(report.Importable) != 0 {
		t.Errorf("expected 0 importable, got %d", len(report.Importable))
	}
	if len(report.SkippedManaged) != 1 {
		t.Errorf("expected 1 managed skip, got %d", len(report.SkippedManaged))
	}
	if report.Proposal != nil {
		t.Errorf("expected no proposal, got %#v", report.Proposal)
	}

	human, _, _ := runImport(t, ctx, "--file", canonical, "--dry-run")
	if strings.Contains(human, "proposal:") {
		t.Errorf("human output should not include proposal: line:\n%s", human)
	}
}

// --- 14. Apply attempt without --dry-run → exitResolveErr -----------

func TestPathImport_RequiresDryRun(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")
	_, _, err := runImport(t, ctx, "--file", canonical)
	if err == nil {
		t.Fatalf("expected error for missing --dry-run")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	if !strings.Contains(ee.msg, "apply flow not yet implemented") {
		t.Errorf("error message lacks 'apply flow not yet implemented': %q", ee.msg)
	}
	if !strings.Contains(ee.msg, "--dry-run") {
		t.Errorf("error message lacks '--dry-run' hint: %q", ee.msg)
	}
}

// --- 15. --dry-run does not write anything (mtime invariance) -------

func TestPathImport_DryRunIsReadOnly(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, `# bashrc
export PATH="/opt/x:$PATH"
`)
	// Backdate mtime so any write would obviously bump it.
	backdated := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(canonical, backdated, backdated); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	beforeInfo, err := os.Stat(canonical)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	beforeBytes, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	if _, _, err := runImport(t, ctx, "--file", canonical, "--dry-run"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	afterInfo, err := os.Stat(canonical)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Errorf("mtime changed: before %s, after %s",
			beforeInfo.ModTime(), afterInfo.ModTime())
	}
	afterBytes, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Errorf("file content changed:\nbefore: %s\nafter:  %s", beforeBytes, afterBytes)
	}
}

// --- bonus: --file on a path that does not exist --------------------
// resolveAliasTarget happily returns an absolute path even for a
// non-existent file; we treat ENOENT as "empty rc" so the sentinel is
// what the user sees rather than a stat error.
func TestPathImport_MissingFileTreatedAsEmpty(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	bogus := filepath.Join(t.TempDir(), "does-not-exist")

	stdout, _, err := runImport(t, ctx, "--file", bogus, "--dry-run")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "no importable PATH lines found.") {
		t.Errorf("expected empty-sentinel for missing file, got:\n%s", stdout)
	}
}
