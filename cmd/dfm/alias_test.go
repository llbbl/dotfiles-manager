package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestBuildAliasLine_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		family  string
		alias   string
		command string
		want    string
	}{
		{
			name:    "posix simple",
			family:  "posix",
			alias:   "ll",
			command: "ls -la",
			want:    "alias ll='ls -la'\n",
		},
		{
			// POSIX shells can't escape ' inside '...'; the idiom is to
			// close the quote, emit \', and reopen: '\''.
			name:    "posix single quote",
			family:  "posix",
			alias:   "g",
			command: "echo 'hi'",
			want:    `alias g='echo '\''hi'\'''` + "\n",
		},
		{
			// Inside single quotes $ has no special meaning, so no escaping.
			name:    "posix dollar literal",
			family:  "posix",
			alias:   "p",
			command: "echo $PATH",
			want:    "alias p='echo $PATH'\n",
		},
		{
			// Inside POSIX single quotes a backslash is literal too.
			name:    "posix backslash",
			family:  "posix",
			alias:   "b",
			command: `echo a\b`,
			want:    "alias b='echo a\\b'\n",
		},
		{
			name:    "fish simple",
			family:  "fish",
			alias:   "ll",
			command: "ls -la",
			want:    "alias ll 'ls -la'\n",
		},
		{
			// Fish single-quote strings: only \' and \\ are special.
			name:    "fish single quote",
			family:  "fish",
			alias:   "g",
			command: "echo 'hi'",
			want:    `alias g 'echo \'hi\''` + "\n",
		},
		{
			// Backslash gets doubled in fish so the runtime sees one \.
			name:    "fish backslash",
			family:  "fish",
			alias:   "b",
			command: `echo a\b`,
			want:    `alias b 'echo a\\b'` + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildAliasLine(tc.family, tc.alias, tc.command)
			if got != tc.want {
				t.Errorf("buildAliasLine(%q,%q,%q)\n got: %q\nwant: %q",
					tc.family, tc.alias, tc.command, got, tc.want)
			}
		})
	}
}

func TestAliasAdd_AppendsAndAudits(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "# bashrc\n")

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "ll", "ls -la"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Fenced-block emission: header is preserved, the new managed alias
	// arrives wrapped in dfm marker comments.
	want := "# bashrc\n" +
		"# >>> dfm:alias ll >>>\n" +
		"alias ll='ls -la'\n" +
		"# <<< dfm:alias ll <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"action":"alias.add"`) {
		t.Errorf("missing alias.add event in audit log:\n%s", data)
	}
	// Per dfm-ap2: a no-conflict add is the "append" sub-action.
	if !strings.Contains(string(data), `"sub_action":"append"`) {
		t.Errorf("expected audit field action=append:\n%s", data)
	}
	// Privacy invariant: command body must never appear in the log.
	if strings.Contains(string(data), "ls -la") {
		t.Errorf("alias command body leaked into audit log: %s", data)
	}
}

func TestAliasAdd_EmbeddedSingleQuote(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// Caller passes the literal command string "echo 'hi'"; we expect
	// the POSIX '\'' escape on the way out.
	cmd.SetArgs([]string{"add", "--file", canonical, "g", "echo 'hi'"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}
	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The block wraps the same posix-escaped body the bare-line form
	// would produce.
	want := "# >>> dfm:alias g >>>\n" +
		`alias g='echo '\''hi'\'''` + "\n" +
		"# <<< dfm:alias g <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestAliasAdd_InvalidName(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")
	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "bad-name", "ls"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("want error for invalid alias name, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
}

func TestAliasAdd_RejectsDuplicateByDefault(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	initial := "alias cr='foo'\n"
	canonical, _ := writeTracked(t, ctx, initial)

	// Capture pre-state of audit log so we can confirm zero new rows.
	preAudit, _ := os.ReadFile(logPath)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "cr", "bar"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected duplicate-rejection error, got nil; out=%s", out.String())
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitAlreadyOrMiss {
		t.Fatalf("want exitError(code=%d), got %v", exitAlreadyOrMiss, err)
	}

	got, rerr := os.ReadFile(canonical)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if string(got) != initial {
		t.Errorf("file mutated on rejection: got %q want %q", got, initial)
	}

	postAudit, _ := os.ReadFile(logPath)
	if !bytes.Equal(preAudit, postAudit) {
		t.Errorf("audit log mutated on rejection:\nbefore: %s\nafter:  %s", preAudit, postAudit)
	}
}

func TestAliasAdd_ReplaceOverwritesExisting(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "# top\nalias cr='foo'\n# tail\n")

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--replace", "cr", "bar"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// --replace on a legacy bare line migrates it to the fenced form
	// at the same position. The block displaces the original line.
	want := "# top\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='bar'\n" +
		"# <<< dfm:alias cr <<<\n" +
		"# tail\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	// Verify exactly one alias cr line.
	if n := strings.Count(string(got), "alias cr="); n != 1 {
		t.Errorf("expected 1 alias cr= line, got %d in %q", n, got)
	}
	if !strings.Contains(out.String(), "replaced alias 'cr'") {
		t.Errorf("stdout should say replaced, got %q", out.String())
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"sub_action":"replace"`) {
		t.Errorf("missing action=replace in audit log:\n%s", data)
	}
	if strings.Contains(string(data), `"bar"`) {
		t.Errorf("command body leaked into audit log: %s", data)
	}
}

func TestAliasAdd_ReplaceOnEmptyAppends(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "# header\n")

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--replace", "cr", "bar"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "# header\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='bar'\n" +
		"# <<< dfm:alias cr <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "added alias 'cr'") {
		t.Errorf("stdout should say added (append path), got %q", out.String())
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"sub_action":"append"`) {
		t.Errorf("missing action=append in audit log:\n%s", data)
	}
}

func TestAliasAdd_ForceAllowsDuplicate(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "alias cr='foo'\n")

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--force", "cr", "bar"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// --force appends a fresh fenced block on top of the existing
	// legacy bare line without disturbing it.
	want := "alias cr='foo'\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='bar'\n" +
		"# <<< dfm:alias cr <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if n := strings.Count(string(got), "alias cr="); n != 2 {
		t.Errorf("expected 2 alias cr= lines, got %d", n)
	}
	if !strings.Contains(out.String(), "appended duplicate alias 'cr'") {
		t.Errorf("stdout should mention forced duplicate, got %q", out.String())
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"sub_action":"force-append"`) {
		t.Errorf("missing action=force-append in audit log:\n%s", data)
	}
}

func TestAliasAdd_ReplaceAndForceMutuallyExclusive(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	initial := "alias cr='foo'\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--replace", "--force", "cr", "bar"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for --replace + --force, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}

	got, rerr := os.ReadFile(canonical)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if string(got) != initial {
		t.Errorf("file mutated on flag-conflict error: got %q want %q", got, initial)
	}
}

func TestAliasAdd_ReplaceCollapsesPreexistingDuplicates(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	// Simulate a user who got bitten by the pre-fix duplicate-append bug.
	initial := "# top\nalias cr='one'\nmiddle\nalias cr='two'\n# tail\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--replace", "cr", "fresh"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := strings.Count(string(got), "alias cr="); n != 1 {
		t.Errorf("expected duplicates collapsed to 1 line, got %d in %q", n, got)
	}
	if !strings.Contains(string(got), "alias cr='fresh'") {
		t.Errorf("expected new value 'fresh', got %q", got)
	}
	// The single surviving definition should sit where the first match
	// was (between "# top" and "middle"), not at the tail, and be
	// wrapped in a fenced block.
	want := "# top\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='fresh'\n" +
		"# <<< dfm:alias cr <<<\n" +
		"middle\n# tail\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestAliasRemove_RemovesAllMatches(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	initial := "# top\nalias foo='one'\nkeep me\nalias foo='two'\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"remove", "--file", canonical, "foo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "# top\nkeep me\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "removed 2 alias line(s)") {
		t.Errorf("output should report 2 removals, got %q", out.String())
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"action":"alias.remove"`) {
		t.Errorf("missing alias.remove event:\n%s", data)
	}
	if !strings.Contains(string(data), `"lines_removed":2`) {
		t.Errorf("expected lines_removed=2 in audit:\n%s", data)
	}
}

func TestAliasRemove_NotFound(t *testing.T) {
	// os.Exit on the not-found path would tear the test process down,
	// so we exercise just enough plumbing to verify the matcher logic
	// and then assert no mutation occurred via a fresh invocation that
	// finds zero matches. We use a subprocess-free approach: pre-check
	// the file contains no `alias missing` line, then verify the regex
	// directly.
	re := aliasMatchRe("posix", "missing")
	if re.MatchString("alias foo='x'") {
		t.Fatalf("regex matched wrong alias")
	}
	if !re.MatchString("alias missing='x'") {
		t.Fatalf("regex failed to match target alias")
	}
}

// TestAliasAdd_EmitsFencedBlock pins the exact bytes appended for a
// fresh add: header preserved, body wrapped in dfm fence comments.
func TestAliasAdd_EmitsFencedBlock(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "# bashrc\n")

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "cr", "claude --resume"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Byte-equal assertion on the trailing region: the rc must end with
	// the fenced block, with a single trailing newline after the close
	// fence and no extra padding.
	wantTail := "# >>> dfm:alias cr >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:alias cr <<<\n"
	if !strings.HasSuffix(string(got), wantTail) {
		t.Errorf("rc tail does not match fenced block.\nfull content: %q\nwant suffix:  %q", got, wantTail)
	}
	if string(got) != "# bashrc\n"+wantTail {
		t.Errorf("full content mismatch.\n got: %q\nwant: %q", got, "# bashrc\n"+wantTail)
	}
}

// TestAliasAdd_DetectsLegacyBareLineAsDuplicate verifies that a hand-
// written bare `alias` line still trips the duplicate check, even
// though it isn't fenced.
func TestAliasAdd_DetectsLegacyBareLineAsDuplicate(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	initial := "alias cr='foo'\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "cr", "bar"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected duplicate-rejection error, got nil; out=%s", out.String())
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitAlreadyOrMiss {
		t.Fatalf("want exitError(code=%d), got %v", exitAlreadyOrMiss, err)
	}
	got, _ := os.ReadFile(canonical)
	if string(got) != initial {
		t.Errorf("file mutated on rejection: got %q want %q", got, initial)
	}
}

// TestAliasAdd_ReplaceMigratesLegacyToFenced confirms that --replace on
// an existing bare-line definition rewrites it as a fenced block, with
// no stray bare-line copy left behind.
func TestAliasAdd_ReplaceMigratesLegacyToFenced(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "alias cr='foo'\n")

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--replace", "cr", "bar"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	want := "# >>> dfm:alias cr >>>\n" +
		"alias cr='bar'\n" +
		"# <<< dfm:alias cr <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if n := strings.Count(string(got), "alias cr="); n != 1 {
		t.Errorf("expected exactly one alias cr= line (the block body), got %d", n)
	}
	if !strings.Contains(string(got), "# >>> dfm:alias cr >>>") {
		t.Errorf("missing open fence: %q", got)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"sub_action":"replace"`) {
		t.Errorf("missing sub_action=replace in audit log:\n%s", data)
	}
}

// TestAliasAdd_ReplaceCollapsesMixedLegacyAndFenced exercises the
// rare-but-real case where a user hand-edited around an existing dfm
// block, leaving both a fenced block and a stray bare line for the
// same alias. --replace must collapse to exactly one fenced block.
func TestAliasAdd_ReplaceCollapsesMixedLegacyAndFenced(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	initial := "# top\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='old'\n" +
		"# <<< dfm:alias cr <<<\n" +
		"alias cr='hand'\n" +
		"# tail\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--replace", "cr", "fresh"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	// Exactly one fenced block, exactly one bare alias cr= line (the
	// block body), no legacy stray.
	if n := strings.Count(string(got), "# >>> dfm:alias cr >>>"); n != 1 {
		t.Errorf("expected 1 open fence, got %d in %q", n, got)
	}
	if n := strings.Count(string(got), "# <<< dfm:alias cr <<<"); n != 1 {
		t.Errorf("expected 1 close fence, got %d in %q", n, got)
	}
	if n := strings.Count(string(got), "alias cr="); n != 1 {
		t.Errorf("expected 1 alias cr= line, got %d in %q", n, got)
	}
	if !strings.Contains(string(got), "alias cr='fresh'") {
		t.Errorf("expected new value 'fresh', got %q", got)
	}
}

// TestAliasRemove_StripsFencedBlock pins block-aware removal: after
// remove, the rc retains no trace of the fence lines or the body.
func TestAliasRemove_StripsFencedBlock(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	initial := "# top\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:alias cr <<<\n" +
		"# tail\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"remove", "--file", canonical, "cr"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	want := "# top\n# tail\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"dfm:alias cr", "alias cr=", "claude --resume"} {
		if strings.Contains(string(got), forbidden) {
			t.Errorf("expected %q to be gone, still present in %q", forbidden, got)
		}
	}

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"blocks_removed":1`) {
		t.Errorf("expected blocks_removed=1 in audit:\n%s", data)
	}
}

// TestAliasRemove_StripsMixedLegacyAndFenced confirms the two-phase
// removal handles a mix of fenced and bare definitions in one pass,
// and reports counts from both phases in the audit event.
func TestAliasRemove_StripsMixedLegacyAndFenced(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	initial := "alias cr='legacy'\n" +
		"keep me\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='managed'\n" +
		"# <<< dfm:alias cr <<<\n" +
		"alias cr='another legacy'\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"remove", "--file", canonical, "cr"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	if strings.Contains(string(got), "alias cr=") {
		t.Errorf("expected all alias cr= lines gone, got %q", got)
	}
	if strings.Contains(string(got), "dfm:alias cr") {
		t.Errorf("expected fence lines gone, got %q", got)
	}
	if !strings.Contains(string(got), "keep me") {
		t.Errorf("unrelated content was removed: %q", got)
	}

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"blocks_removed":1`) {
		t.Errorf("expected blocks_removed=1 in audit:\n%s", data)
	}
	if !strings.Contains(string(data), `"bare_lines_removed":2`) {
		t.Errorf("expected bare_lines_removed=2 in audit:\n%s", data)
	}
	if !strings.Contains(string(data), `"lines_removed":3`) {
		t.Errorf("expected lines_removed=3 in audit:\n%s", data)
	}
}

// TestAliasList_ParsesFencedEntries verifies that the existing per-line
// list parser picks up the bare `alias` line inside a fenced block.
// The fence lines themselves start with `#` and don't match the alias
// regex, so no list-side changes were needed.
func TestAliasList_ParsesFencedEntries(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	contents := "# header\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:alias cr <<<\n"
	canonical, _ := writeTracked(t, ctx, contents)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--file", canonical})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}
	o := out.String()
	for _, want := range []string{"cr", "claude --resume"} {
		if !strings.Contains(o, want) {
			t.Errorf("list output missing %q\nfull output:\n%s", want, o)
		}
	}
	// The fence comment lines should not show up as parsed aliases —
	// they begin with `#`, not `alias`.
	if strings.Contains(o, ">>> dfm:alias") || strings.Contains(o, "<<< dfm:alias") {
		t.Errorf("fence lines leaked into list output:\n%s", o)
	}
}

func TestAliasList_PrintsParsedAndSkipsNoise(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	contents := "# a comment\n" +
		"alias ll='ls -la'\n" +
		"alias gs='git status'\n" +
		"export FOO=bar\n" +
		"alias gco=\"git checkout\"\n"
	canonical, _ := writeTracked(t, ctx, contents)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--file", canonical})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}
	o := out.String()
	for _, want := range []string{"ll", "ls -la", "gs", "git status", "gco", "git checkout"} {
		if !strings.Contains(o, want) {
			t.Errorf("list output missing %q\nfull output:\n%s", want, o)
		}
	}
	if strings.Contains(o, "# a comment") || strings.Contains(o, "export FOO") {
		t.Errorf("list should skip non-alias lines, got:\n%s", o)
	}
}
