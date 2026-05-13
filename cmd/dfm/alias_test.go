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
	// Shared-block emission: header preserved; the new managed alias
	// lands inside the default `dfm:aliases` block.
	want := "# bashrc\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias ll='ls -la'\n" +
		"# <<< dfm:aliases <<<\n"
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
	if !strings.Contains(string(data), `"sub_action":"append"`) {
		t.Errorf("expected audit field sub_action=append:\n%s", data)
	}
	if !strings.Contains(string(data), `"block_action":"created"`) {
		t.Errorf("expected audit field block_action=created:\n%s", data)
	}
	if !strings.Contains(string(data), `"group":"aliases"`) {
		t.Errorf("expected audit field group=aliases:\n%s", data)
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
	cmd.SetArgs([]string{"add", "--file", canonical, "g", "echo 'hi'"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}
	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "# >>> dfm:aliases >>>\n" +
		`alias g='echo '\''hi'\'''` + "\n" +
		"# <<< dfm:aliases <<<\n"
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
	// --replace strips every prior definition (here: one bare line) and
	// inserts the new entry into the default shared block at EOF.
	want := "# top\n# tail\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='bar'\n" +
		"# <<< dfm:aliases <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
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
		t.Errorf("missing sub_action=replace in audit log:\n%s", data)
	}
	if !strings.Contains(string(data), `"block_action":"replaced"`) {
		t.Errorf("missing block_action=replaced in audit log:\n%s", data)
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
		"# >>> dfm:aliases >>>\n" +
		"alias cr='bar'\n" +
		"# <<< dfm:aliases <<<\n"
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
		t.Errorf("missing sub_action=append in audit log:\n%s", data)
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
	// --force appends a fresh default block on top of the existing
	// legacy bare line without disturbing it.
	want := "alias cr='foo'\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='bar'\n" +
		"# <<< dfm:aliases <<<\n"
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
		t.Errorf("missing sub_action=force-append in audit log:\n%s", data)
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
	want := "# top\nmiddle\n# tail\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='fresh'\n" +
		"# <<< dfm:aliases <<<\n"
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
	re := aliasMatchRe("posix", "missing")
	if re.MatchString("alias foo='x'") {
		t.Fatalf("regex matched wrong alias")
	}
	if !re.MatchString("alias missing='x'") {
		t.Fatalf("regex failed to match target alias")
	}
}

// TestAliasAdd_CreatesDefaultBlock — first `add` creates the `# >>> dfm:aliases >>>`
// block with one entry.
func TestAliasAdd_CreatesDefaultBlock(t *testing.T) {
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

	got, _ := os.ReadFile(canonical)
	want := "# bashrc\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:aliases <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestAliasAdd_AppendsIntoExistingDefaultBlock — second `add` adds a body
// line inside the existing block, not a new block.
func TestAliasAdd_AppendsIntoExistingDefaultBlock(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "# bashrc\n")

	for _, args := range [][]string{
		{"add", "--file", canonical, "cr", "claude --resume"},
		{"add", "--file", canonical, "ll", "ls -lah"},
	} {
		cmd := newAliasCmd()
		cmd.SetContext(ctx)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute %v: %v\nout: %s", args, err, out.String())
		}
	}

	got, _ := os.ReadFile(canonical)
	want := "# bashrc\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"alias ll='ls -lah'\n" +
		"# <<< dfm:aliases <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if n := strings.Count(string(got), "# >>> dfm:aliases >>>"); n != 1 {
		t.Errorf("expected exactly 1 open fence, got %d", n)
	}

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"block_action":"appended"`) {
		t.Errorf("expected block_action=appended on second add:\n%s", data)
	}
}

// TestAliasAdd_NamedGroupCreatesGroupBlock — `add --group terraform tf
// "terraform"` creates the `# >>> dfm:group terraform >>>` block.
func TestAliasAdd_NamedGroupCreatesGroupBlock(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--group", "terraform", "tf", "terraform"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	want := "# >>> dfm:group terraform >>>\n" +
		"alias tf='terraform'\n" +
		"# <<< dfm:group terraform <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestAliasAdd_NamedGroupAppendsIntoExistingGroupBlock — second
// `add --group terraform` appends inside.
func TestAliasAdd_NamedGroupAppendsIntoExistingGroupBlock(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	for _, args := range [][]string{
		{"add", "--file", canonical, "--group", "terraform", "tf", "terraform"},
		{"add", "--file", canonical, "--group", "terraform", "tfa", "terraform apply -auto-approve"},
		{"add", "--file", canonical, "--group", "terraform", "tfplan", "terraform plan"},
	} {
		cmd := newAliasCmd()
		cmd.SetContext(ctx)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute %v: %v\nout: %s", args, err, out.String())
		}
	}

	got, _ := os.ReadFile(canonical)
	want := "# >>> dfm:group terraform >>>\n" +
		"alias tf='terraform'\n" +
		"alias tfa='terraform apply -auto-approve'\n" +
		"alias tfplan='terraform plan'\n" +
		"# <<< dfm:group terraform <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if n := strings.Count(string(got), "# >>> dfm:group terraform >>>"); n != 1 {
		t.Errorf("expected exactly 1 terraform fence, got %d", n)
	}
}

// TestAliasAdd_DefaultAndGroupCoexist — adds without group and with
// `--group` produce both blocks side by side.
func TestAliasAdd_DefaultAndGroupCoexist(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	for _, args := range [][]string{
		{"add", "--file", canonical, "cr", "claude --resume"},
		{"add", "--file", canonical, "ll", "ls -lah"},
		{"add", "--file", canonical, "--group", "terraform", "tf", "terraform"},
		{"add", "--file", canonical, "--group", "terraform", "tfa", "terraform apply -auto-approve"},
		{"add", "--file", canonical, "--group", "terraform", "tfplan", "terraform plan"},
	} {
		cmd := newAliasCmd()
		cmd.SetContext(ctx)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute %v: %v\nout: %s", args, err, out.String())
		}
	}

	got, _ := os.ReadFile(canonical)
	want := "# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"alias ll='ls -lah'\n" +
		"# <<< dfm:aliases <<<\n" +
		"# >>> dfm:group terraform >>>\n" +
		"alias tf='terraform'\n" +
		"alias tfa='terraform apply -auto-approve'\n" +
		"alias tfplan='terraform plan'\n" +
		"# <<< dfm:group terraform <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestAliasAdd_RejectsBadGroupName — `--group "has space"` exits
// `exitResolveErr`, no mutation.
func TestAliasAdd_RejectsBadGroupName(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	initial := ""
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--group", "has space", "tf", "terraform"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for bad group name, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	got, _ := os.ReadFile(canonical)
	if string(got) != initial {
		t.Errorf("file mutated on bad-group rejection: %q", got)
	}
}

// TestAliasAdd_DetectsDupeAcrossBlocks — alias `cr` in default block;
// `add cr ... --group foo` (no `--replace`) is rejected as duplicate.
func TestAliasAdd_DetectsDupeAcrossBlocks(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	initial := "# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:aliases <<<\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--group", "foo", "cr", "other"})
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
		t.Errorf("file mutated on cross-block dupe rejection: %q", got)
	}
}

// TestAliasAdd_ReplaceMovesBetweenBlocks — alias `cr` in default block;
// `add cr ... --group foo --replace` removes it from default block and
// inserts it into the foo group.
func TestAliasAdd_ReplaceMovesBetweenBlocks(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	initial := "# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"alias ll='ls -lah'\n" +
		"# <<< dfm:aliases <<<\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--group", "anth", "--replace", "cr", "claude code"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	want := "# >>> dfm:aliases >>>\n" +
		"alias ll='ls -lah'\n" +
		"# <<< dfm:aliases <<<\n" +
		"# >>> dfm:group anth >>>\n" +
		"alias cr='claude code'\n" +
		"# <<< dfm:group anth <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestAliasAdd_ReplaceMigratesLegacyBlockToShared — pre-seed a legacy
// `# >>> dfm:alias cr >>>` block; `add cr ... --replace` produces a
// `# >>> dfm:aliases >>>` block containing the entry and no legacy block.
func TestAliasAdd_ReplaceMigratesLegacyBlockToShared(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	initial := "# top\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='old'\n" +
		"# <<< dfm:alias cr <<<\n" +
		"# tail\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--replace", "cr", "claude --resume"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	want := "# top\n# tail\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:aliases <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if strings.Contains(string(got), "dfm:alias cr") {
		t.Errorf("legacy fence not migrated, still present: %q", got)
	}
}

// TestAliasAdd_ForceAppendsDuplicateInTargetBlock — pre-seed default
// block with one `cr`; `add cr ... --force` results in two `alias cr=`
// lines inside the same default block.
func TestAliasAdd_ForceAppendsDuplicateInTargetBlock(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	initial := "# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:aliases <<<\n"
	canonical, _ := writeTracked(t, ctx, initial)

	cmd := newAliasCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--file", canonical, "--force", "cr", "claude code"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	got, _ := os.ReadFile(canonical)
	want := "# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"alias cr='claude code'\n" +
		"# <<< dfm:aliases <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestAliasRemove_DropsLineFromDefaultBlock — multi-entry default block,
// remove one entry, block survives with the others.
func TestAliasRemove_DropsLineFromDefaultBlock(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	initial := "# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"alias ll='ls -lah'\n" +
		"# <<< dfm:aliases <<<\n"
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
	want := "# >>> dfm:aliases >>>\n" +
		"alias ll='ls -lah'\n" +
		"# <<< dfm:aliases <<<\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"shared_block_evictions":1`) {
		t.Errorf("expected shared_block_evictions=1 in audit:\n%s", data)
	}
	if !strings.Contains(string(data), `"empty_blocks_dropped":0`) {
		t.Errorf("expected empty_blocks_dropped=0 in audit:\n%s", data)
	}
}

// TestAliasRemove_DropsEmptyBlockWhenLastEntryLeaves — single-entry
// default block, remove the entry, entire block (fences included) is gone.
func TestAliasRemove_DropsEmptyBlockWhenLastEntryLeaves(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	initial := "# top\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:aliases <<<\n" +
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
	for _, forbidden := range []string{"dfm:aliases", "alias cr=", "claude --resume"} {
		if strings.Contains(string(got), forbidden) {
			t.Errorf("expected %q to be gone, still present in %q", forbidden, got)
		}
	}

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"empty_blocks_dropped":1`) {
		t.Errorf("expected empty_blocks_dropped=1 in audit:\n%s", data)
	}
}

// TestAliasRemove_StripsAcrossLegacyAndShared — mixed input (legacy
// block + entry in default block + bare line) all removed in one pass.
func TestAliasRemove_StripsAcrossLegacyAndShared(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	initial := "alias cr='legacy'\n" +
		"keep me\n" +
		"# >>> dfm:alias cr >>>\n" +
		"alias cr='legacy-block'\n" +
		"# <<< dfm:alias cr <<<\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='managed'\n" +
		"alias ll='ls -lah'\n" +
		"# <<< dfm:aliases <<<\n"
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
		t.Errorf("expected legacy fence gone, got %q", got)
	}
	if !strings.Contains(string(got), "alias ll='ls -lah'") {
		t.Errorf("unrelated shared-block entry was lost: %q", got)
	}
	if !strings.Contains(string(got), "keep me") {
		t.Errorf("unrelated content was removed: %q", got)
	}

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"blocks_removed":1`) {
		t.Errorf("expected blocks_removed=1 in audit:\n%s", data)
	}
	if !strings.Contains(string(data), `"shared_block_evictions":1`) {
		t.Errorf("expected shared_block_evictions=1 in audit:\n%s", data)
	}
	if !strings.Contains(string(data), `"bare_lines_removed":1`) {
		t.Errorf("expected bare_lines_removed=1 in audit:\n%s", data)
	}
	if !strings.Contains(string(data), `"lines_removed":3`) {
		t.Errorf("expected lines_removed=3 in audit:\n%s", data)
	}
}

// TestAliasList_ParsesEntriesFromBothBlockKinds — `list` reports entries
// from the default block and named groups.
func TestAliasList_ParsesEntriesFromBothBlockKinds(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	contents := "# header\n" +
		"# >>> dfm:aliases >>>\n" +
		"alias cr='claude --resume'\n" +
		"# <<< dfm:aliases <<<\n" +
		"# >>> dfm:group terraform >>>\n" +
		"alias tf='terraform'\n" +
		"alias tfplan='terraform plan'\n" +
		"# <<< dfm:group terraform <<<\n"
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
	for _, want := range []string{"cr", "claude --resume", "tf", "terraform", "tfplan", "terraform plan"} {
		if !strings.Contains(o, want) {
			t.Errorf("list output missing %q\nfull output:\n%s", want, o)
		}
	}
	if strings.Contains(o, ">>> dfm:") || strings.Contains(o, "<<< dfm:") {
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
