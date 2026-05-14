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

	"github.com/llbbl/dotfiles-manager/internal/ai"
	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// fakeProvider returns canned outputs and records whether Suggest was called.
type fakeProvider struct {
	diff       string
	summary    string
	askText    string
	err        error
	suggestHit bool
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Ask(_ context.Context, _ ai.AskRequest) (ai.AskResponse, error) {
	if f.err != nil {
		return ai.AskResponse{}, f.err
	}
	return ai.AskResponse{Text: f.askText, Duration: time.Millisecond}, nil
}

func (f *fakeProvider) Suggest(_ context.Context, _ ai.SuggestRequest) (ai.SuggestResponse, error) {
	f.suggestHit = true
	if f.err != nil {
		return ai.SuggestResponse{}, f.err
	}
	return ai.SuggestResponse{Diff: f.diff, Summary: f.summary, Duration: time.Millisecond}, nil
}

func setupSuggestEnv(t *testing.T) (*config.Config, *store.Store, string) {
	t.Helper()
	root := t.TempDir()
	logPath := filepath.Join(root, "logs", "actions.jsonl")
	cfg := &config.Config{
		Log:   config.LogConfig{Path: logPath, Backend: "both"},
		State: config.StateConfig{URL: "file://" + filepath.Join(root, "state.db")},
	}
	cfg.AI.Provider = "claude-code"

	ctx := context.Background()
	s, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	logger, err := audit.New(ctx, cfg, s)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	audit.SetDefault(logger)
	t.Cleanup(func() {
		_ = logger.Close()
		audit.SetDefault(nil)
	})
	return cfg, s, logPath
}

func swapProvider(t *testing.T, p ai.Provider) {
	t.Helper()
	prev := providerFactory
	providerFactory = func(*config.Config) (ai.Provider, error) { return p, nil }
	t.Cleanup(func() { providerFactory = prev })
}

func TestSuggest_WritesPendingRow(t *testing.T) {
	cfg, s, logPath := setupSuggestEnv(t)
	ctx := config.WithContext(context.Background(), cfg)

	fixture := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(fixture, []byte("# fixture\nfoo=bar\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	canonical, display, err := tracker.Resolve(fixture)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := tracker.Track(ctx, s, canonical, display,
		tracker.TrackOptions{SkipSecretCheck: true}); err != nil {
		t.Fatalf("track: %v", err)
	}

	cannedDiff := "--- a/fixture.txt\n+++ b/fixture.txt\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=\"bar\"\n"
	fake := &fakeProvider{diff: cannedDiff, summary: "Quote the value."}
	swapProvider(t, fake)

	cmd := newSuggestCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{fixture})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var gotDiff, gotStatus string
	row := s.DB().QueryRowContext(ctx,
		`SELECT diff, status FROM suggestions ORDER BY created_at DESC LIMIT 1`)
	if err := row.Scan(&gotDiff, &gotStatus); err != nil {
		t.Fatalf("scan suggestions: %v", err)
	}
	if gotStatus != "pending" {
		t.Errorf("status = %q", gotStatus)
	}
	if gotDiff != cannedDiff {
		t.Errorf("diff = %q", gotDiff)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if !strings.Contains(string(data), `"action":"suggest"`) {
		t.Errorf("jsonl missing suggest action: %s", data)
	}
	if strings.Contains(string(data), "foo=bar") || strings.Contains(string(data), cannedDiff) {
		t.Errorf("audit log contains file/diff content: %s", data)
	}
}

func TestSuggest_UntrackedExits4(t *testing.T) {
	cfg, _, _ := setupSuggestEnv(t)
	ctx := config.WithContext(context.Background(), cfg)

	fixture := filepath.Join(t.TempDir(), "untracked.txt")
	if err := os.WriteFile(fixture, []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	fake := &fakeProvider{diff: "--- a/x\n", summary: "x"}
	swapProvider(t, fake)

	cmd := newSuggestCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{fixture})
	cmd.SilenceErrors = true
	err := cmd.Execute()
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitNotFound {
		t.Errorf("exit code = %d, want %d", ee.code, exitNotFound)
	}
	if fake.suggestHit {
		t.Error("provider should not have been called for an untracked file")
	}
}

// trackFixtureForSuggest creates a small tracked fixture file and returns
// its path. Shared setup for the malformed-diff tests below.
func trackFixtureForSuggest(t *testing.T, ctx context.Context, s *store.Store) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(fixture, []byte("# fixture\nfoo=bar\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	canonical, display, err := tracker.Resolve(fixture)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := tracker.Track(ctx, s, canonical, display,
		tracker.TrackOptions{SkipSecretCheck: true}); err != nil {
		t.Fatalf("track: %v", err)
	}
	return fixture
}

func TestSuggest_MalformedHunkHeaderRefusesWrite(t *testing.T) {
	cfg, s, _ := setupSuggestEnv(t)
	ctx := config.WithContext(context.Background(), cfg)
	fixture := trackFixtureForSuggest(t, ctx, s)

	// Bare "@@" hunk header — the real-world failure that motivated this.
	badDiff := "--- a/fixture.txt\n+++ b/fixture.txt\n@@\n # fixture\n-foo=bar\n+foo=\"bar\"\n"
	fake := &fakeProvider{diff: badDiff, summary: "x"}
	swapProvider(t, fake)

	cmd := newSuggestCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{fixture})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()

	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitDiffMalformed {
		t.Errorf("exit code = %d, want %d", ee.code, exitDiffMalformed)
	}
	if !strings.Contains(ee.msg, "malformed") {
		t.Errorf("err msg missing 'malformed': %q", ee.msg)
	}
	if !strings.Contains(ee.msg, "hunk header") {
		t.Errorf("err msg missing 'hunk header': %q", ee.msg)
	}

	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM suggestions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("suggestions row count = %d, want 0", count)
	}
}

func TestSuggest_NonDiffProseRefusesWrite(t *testing.T) {
	cfg, s, _ := setupSuggestEnv(t)
	ctx := config.WithContext(context.Background(), cfg)
	fixture := trackFixtureForSuggest(t, ctx, s)

	fake := &fakeProvider{
		diff:    "I'm sorry, I can't produce a diff for this file.\n",
		summary: "x",
	}
	swapProvider(t, fake)

	cmd := newSuggestCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{fixture})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()

	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitDiffMalformed {
		t.Errorf("exit code = %d, want %d", ee.code, exitDiffMalformed)
	}

	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM suggestions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("suggestions row count = %d, want 0", count)
	}
}

func TestSuggest_MalformedJSONModeEmitsError(t *testing.T) {
	cfg, s, _ := setupSuggestEnv(t)
	ctx := config.WithContext(context.Background(), cfg)
	fixture := trackFixtureForSuggest(t, ctx, s)

	badDiff := "--- a/fixture.txt\n+++ b/fixture.txt\n@@\n # fixture\n-foo=bar\n+foo=\"bar\"\n"
	fake := &fakeProvider{diff: badDiff, summary: "x"}
	swapProvider(t, fake)

	var out bytes.Buffer
	cmd := newSuggestCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json", fixture})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()

	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitDiffMalformed {
		t.Errorf("exit code = %d, want %d", ee.code, exitDiffMalformed)
	}

	var payload map[string]any
	if jerr := json.Unmarshal(out.Bytes(), &payload); jerr != nil {
		t.Fatalf("json decode: %v\nstdout: %s", jerr, out.String())
	}
	if payload["error"] != "diff_malformed" {
		t.Errorf("error field = %v, want diff_malformed", payload["error"])
	}
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "hunk header") {
		t.Errorf("message missing 'hunk header': %q", msg)
	}

	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM suggestions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("suggestions row count = %d, want 0", count)
	}
}

func TestSuggest_OversizeFileSkipsProvider(t *testing.T) {
	cfg, s, _ := setupSuggestEnv(t)
	ctx := config.WithContext(context.Background(), cfg)

	fixture := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(fixture, []byte("# small\n"), 0o644); err != nil {
		t.Fatalf("write small: %v", err)
	}
	canonical, display, err := tracker.Resolve(fixture)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := tracker.Track(ctx, s, canonical, display,
		tracker.TrackOptions{SkipSecretCheck: true}); err != nil {
		t.Fatalf("track: %v", err)
	}
	// Grow file past the 1 MiB cap.
	big := make([]byte, (1<<20)+128)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(fixture, big, 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}

	fake := &fakeProvider{diff: "--- a/x\n", summary: "x"}
	swapProvider(t, fake)

	cmd := newSuggestCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{fixture})
	cmd.SilenceErrors = true
	err = cmd.Execute()
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitResolveErr {
		t.Errorf("exit code = %d, want %d", ee.code, exitResolveErr)
	}
	if fake.suggestHit {
		t.Error("provider should not have been called for an oversize file")
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM suggestions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("suggestions row count = %d, want 0", count)
	}
}
