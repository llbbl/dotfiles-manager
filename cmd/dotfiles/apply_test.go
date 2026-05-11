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

	"github.com/llbbl/dotfiles-manager/internal/apply"
	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/ids"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

func setupApplyCmdEnv(t *testing.T) (context.Context, *store.Store, *config.Config, string) {
	t.Helper()
	root := t.TempDir()
	logPath := filepath.Join(root, "logs", "actions.jsonl")
	cfg := &config.Config{
		Log:    config.LogConfig{Path: logPath, Backend: "both"},
		State:  config.StateConfig{URL: "file://" + filepath.Join(root, "state.db")},
		Backup: config.BackupConfig{Dir: filepath.Join(root, "backups")},
	}
	cfg.AI.Provider = "claude-code"

	ctx := context.Background()
	s, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	l, err := audit.New(ctx, cfg, s)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	audit.SetDefault(l)
	t.Cleanup(func() {
		_ = l.Close()
		audit.SetDefault(nil)
	})
	return ctx, s, cfg, logPath
}

func insertSuggestionCmd(t *testing.T, ctx context.Context, s *store.Store, fileID int64, diff string) string {
	t.Helper()
	id, err := ids.New()
	if err != nil {
		t.Fatalf("ids: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO suggestions (id, file_id, provider, prompt, diff, status, created_at)
		 VALUES (?, ?, 'fake', 'file: x\ngoal: t', ?, 'pending', ?)`,
		id, fileID, diff, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
}

func TestApplyCmd_HappyPath(t *testing.T) {
	ctx, s, cfg, logPath := setupApplyCmdEnv(t)
	ctx = config.WithContext(ctx, cfg)

	fix := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(fix, []byte("# fixture\nfoo=bar\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	canonical, display, _ := tracker.Resolve(fix)
	file, err := tracker.Track(ctx, s, canonical, display, tracker.TrackOptions{SkipSecretCheck: true})
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	diff := "--- a/fixture.txt\n+++ b/fixture.txt\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=baz\n"
	id := insertSuggestionCmd(t, ctx, s, file.ID, diff)

	cmd := newApplyCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--yes", "--json", id})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res apply.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v\nout: %s", err, out.String())
	}
	if res.HunksApplied != 1 {
		t.Errorf("hunks = %d", res.HunksApplied)
	}
	if res.SnapshotID == "" {
		t.Error("snapshot_id empty")
	}

	got, _ := os.ReadFile(fix)
	if string(got) != "# fixture\nfoo=baz\n" {
		t.Errorf("file = %q", got)
	}

	// Re-applying must fail with exitAlreadyOrMiss.
	cmd2 := newApplyCmd()
	cmd2.SetContext(ctx)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"--yes", id})
	err2 := cmd2.Execute()
	var ee *exitError
	if !errors.As(err2, &ee) {
		t.Fatalf("want exitError, got %v", err2)
	}
	if ee.code != exitAlreadyOrMiss {
		t.Errorf("exit = %d, want %d", ee.code, exitAlreadyOrMiss)
	}

	// Audit log: contains apply action, no diff body, no file content.
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"action":"apply"`) {
		t.Errorf("audit missing apply action: %s", data)
	}
	if strings.Contains(string(data), "foo=bar") || strings.Contains(string(data), "foo=baz") {
		t.Errorf("audit leaks content: %s", data)
	}
}

func TestRejectCmd_SetsStatusAndLogs(t *testing.T) {
	ctx, s, cfg, logPath := setupApplyCmdEnv(t)
	ctx = config.WithContext(ctx, cfg)

	fix := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(fix, []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	canonical, display, _ := tracker.Resolve(fix)
	file, _ := tracker.Track(ctx, s, canonical, display, tracker.TrackOptions{SkipSecretCheck: true})
	id := insertSuggestionCmd(t, ctx, s, file.ID, "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-hi\n+hello\n")

	cmd := newRejectCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{id})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "rejected") {
		t.Errorf("out = %q", out.String())
	}
	var status string
	_ = s.DB().QueryRowContext(ctx, `SELECT status FROM suggestions WHERE id = ?`, id).Scan(&status)
	if status != apply.StatusRejected {
		t.Errorf("status = %q", status)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"action":"reject"`) {
		t.Errorf("audit missing reject: %s", data)
	}
}

func TestLogCmd_FilterSuggestion(t *testing.T) {
	ctx, s, cfg, _ := setupApplyCmdEnv(t)
	ctx = config.WithContext(ctx, cfg)
	l := audit.Default()

	sid := "test-sug-id-1"
	if err := l.Log(ctx, "suggest", map[string]any{"suggestion_id": sid, "n": 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := l.Log(ctx, "apply", map[string]any{"suggestion_id": sid, "n": 2}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(ctx, "track", map[string]any{"display_path": "~/x.txt"}); err != nil {
		t.Fatal(err)
	}
	_ = s

	cmd := newLogCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--suggestion", sid, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal: %v\nout: %s", err, out.String())
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %s", len(entries), out.String())
	}
	if entries[0]["action"] != "suggest" || entries[1]["action"] != "apply" {
		t.Errorf("order/actions = %v / %v", entries[0]["action"], entries[1]["action"])
	}
	for _, e := range entries {
		if e["action"] == "track" {
			t.Error("track event leaked into suggestion filter")
		}
	}
}
