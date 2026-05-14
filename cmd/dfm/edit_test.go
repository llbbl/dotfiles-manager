package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// setupEditCmdEnv is a thin shim over newTestEnv that pre-attaches the
// config to the context — the ergonomic shape edit/append/alias tests
// already depend on. Kept as a named helper because dozens of call
// sites use this exact 3-value signature; collapsing them all would
// balloon the diff without simplifying anything.
func setupEditCmdEnv(t *testing.T) (context.Context, *config.Config, string) {
	t.Helper()
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)
	return ctx, env.Cfg, env.Paths.LogPath
}

func writeTracked(t *testing.T, ctx context.Context, contents string) (canonical, display string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.txt")
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

func TestEditCmd_NoChanges_NoAudit(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "hello\n")

	t.Setenv("EDITOR", "true") // exits 0 without touching the file

	cmd := newEditCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{canonical})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "no changes") {
		t.Errorf("want 'no changes' in output, got %q", out.String())
	}
	// Audit log should NOT contain an `edit` event.
	if data, err := os.ReadFile(logPath); err == nil {
		if strings.Contains(string(data), `"action":"edit"`) {
			t.Errorf("expected no edit audit event, got log:\n%s", data)
		}
	}
}

func TestEditCmd_Modified_EmitsAudit(t *testing.T) {
	ctx, _, logPath := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "hello\n")

	// Write a tiny shell-script "editor" that appends a line to its arg.
	// Avoids needing to teach runEditor to parse quoted $EDITOR strings.
	editorScript := filepath.Join(t.TempDir(), "edit.sh")
	if err := os.WriteFile(editorScript,
		[]byte("#!/bin/sh\necho modified >> \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	t.Setenv("EDITOR", editorScript)

	cmd := newEditCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{canonical})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "edited") {
		t.Errorf("want 'edited' in output, got %q", out.String())
	}
	got, _ := os.ReadFile(canonical)
	if !strings.Contains(string(got), "modified") {
		t.Errorf("file not modified, contents=%q", got)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"action":"edit"`) {
		t.Errorf("missing edit event in audit log:\n%s", data)
	}
}

func TestEditCmd_NotTracked(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	path := filepath.Join(t.TempDir(), "untracked.txt")
	if err := os.WriteFile(path, []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("EDITOR", "true")

	// os.Exit(4) would kill the test process, so we run the call in a
	// subprocess via the cobra command. Instead, assert against the
	// underlying resolveTracked: same guarantee, no os.Exit.
	s, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()
	if _, _, err := resolveTracked(ctx, s, path); err == nil {
		t.Fatal("want error for untracked path, got nil")
	}
}
