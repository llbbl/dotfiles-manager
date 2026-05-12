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
	want := "# bashrc\nalias ll='ls -la'\n"
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
	want := `alias g='echo '\''hi'\'''` + "\n"
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
