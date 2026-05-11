package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/llbbl/dotfiles-manager/internal/vcs"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func newBareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init --bare: %v: %s", err, out)
	}
	return dir
}

func TestSync_FirstRunCommitsAndPushes(t *testing.T) {
	requireGit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	root := t.TempDir()
	remote := newBareRemote(t)
	local := filepath.Join(root, "backup-local")
	logPath := filepath.Join(root, "logs", "actions.jsonl")
	statePath := filepath.Join(root, "state.db")
	backupDir := filepath.Join(root, "backups")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := &config.Config{
		Repo:   config.RepoConfig{Remote: remote, Local: local},
		Log:    config.LogConfig{Path: logPath},
		State:  config.StateConfig{URL: "file://" + statePath},
		Backup: config.BackupConfig{Dir: backupDir, MaxTotalMB: 500, RetentionDays: 90},
	}

	s, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	logger, err := audit.New(ctx, cfg, s)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	defer logger.Close()
	audit.SetDefault(logger)
	defer audit.SetDefault(nil)

	// InitLocal the backup repo and push an initial commit so origin has refs.
	repo, err := vcs.InitLocal(ctx, cfg)
	if err != nil {
		t.Fatalf("InitLocal: %v", err)
	}
	if err := repo.WriteFile("README.md", []byte("# backup\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := repo.CommitAll(ctx, "init"); err != nil {
		t.Fatalf("CommitAll init: %v", err)
	}
	if err := repo.Push(ctx); err != nil {
		t.Fatalf("Push init: %v", err)
	}

	// Track two files: one under fake $HOME, one elsewhere.
	a := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(a, []byte("# zshrc v1\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	b := filepath.Join(t.TempDir(), "elsewhere.conf")
	if err := os.WriteFile(b, []byte("elsewhere v1\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	for _, p := range []string{a, b} {
		canonical, display, err := tracker.Resolve(p)
		if err != nil {
			t.Fatalf("resolve %s: %v", p, err)
		}
		if _, err := tracker.Track(ctx, s, canonical, display,
			tracker.TrackOptions{SkipSecretCheck: true}); err != nil {
			t.Fatalf("track %s: %v", p, err)
		}
	}

	// Run sync.
	if err := runSync(ctx, cfg); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Assert: backup repo has both files at sanitized paths.
	if _, err := os.Stat(filepath.Join(local, "files", ".zshrc")); err != nil {
		t.Errorf("expected files/.zshrc in backup: %v", err)
	}
	bCanonical, _, _ := tracker.Resolve(b)
	bRel := filepath.Join("files", "_abs", strings.TrimPrefix(filepath.Clean(bCanonical), "/"))
	if _, err := os.Stat(filepath.Join(local, bRel)); err != nil {
		t.Errorf("expected %s in backup: %v", bRel, err)
	}

	// Assert: a commit landed.
	commits, err := repo.Log(ctx, 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) < 2 {
		t.Fatalf("want >=2 commits, got %d", len(commits))
	}
	if !strings.HasPrefix(commits[0].Subject, "sync:") {
		t.Errorf("top commit subject = %q", commits[0].Subject)
	}

	// Assert: ReasonPreSync snapshot rows exist.
	var presyncCount int
	row := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM snapshots WHERE reason = 'pre-sync'`)
	if err := row.Scan(&presyncCount); err != nil {
		t.Fatalf("scan presync count: %v", err)
	}
	if presyncCount != 2 {
		t.Errorf("pre-sync snapshots = %d, want 2", presyncCount)
	}

	// Assert: JSONL has sync entries (local Log.Path).
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read local jsonl: %v", err)
	}
	if !strings.Contains(string(data), `"action":"sync"`) {
		t.Errorf("local jsonl missing sync entry: %s", data)
	}
	if !strings.Contains(string(data), `"action":"sync.file"`) {
		t.Errorf("local jsonl missing sync.file entry")
	}

	// Assert: backup-repo JSONL has sync.file entries.
	backupLog, err := os.ReadFile(filepath.Join(local, "logs", "actions.jsonl"))
	if err != nil {
		t.Fatalf("read backup jsonl: %v", err)
	}
	if !strings.Contains(string(backupLog), `"action":"sync.file"`) {
		t.Errorf("backup jsonl missing sync.file entry")
	}

	// Assert: libSQL actions has sync rows.
	var actionCount int
	row = s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM actions WHERE action = 'sync.file'`)
	if err := row.Scan(&actionCount); err != nil {
		t.Fatalf("scan actions: %v", err)
	}
	if actionCount != 2 {
		t.Errorf("actions sync.file = %d, want 2", actionCount)
	}

	// Mutate one and re-sync.
	if err := os.WriteFile(a, []byte("# zshrc v2\n"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := runSync(ctx, cfg); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	commits, _ = repo.Log(ctx, 10)
	if len(commits) < 3 {
		t.Fatalf("want >=3 commits after second sync, got %d", len(commits))
	}
	// Check sanitized contents updated.
	got, _ := os.ReadFile(filepath.Join(local, "files", ".zshrc"))
	if !strings.Contains(string(got), "v2") {
		t.Errorf("backup not updated, contents = %q", got)
	}
}

// runSync invokes the sync flow directly without going through cobra TTY logic.
func runSync(ctx context.Context, cfg *config.Config) error {
	// Use the exported sync command and execute it via cobra in isolation.
	cmd := newSyncCmd()
	cmd.SetContext(config.WithContext(ctx, cfg))
	cmd.SetArgs([]string{})
	return cmd.Execute()
}

func TestSanitizeBackupPath(t *testing.T) {
	home := "/home/u"
	cases := []struct {
		in, want string
	}{
		{"/home/u/.zshrc", "files/.zshrc"},
		{"/home/u/.config/nvim/init.lua", "files/.config/nvim/init.lua"},
		{"/etc/foo", "files/_abs/etc/foo"},
	}
	for _, tc := range cases {
		got, err := sanitizeBackupPath(tc.in, home)
		if err != nil {
			t.Errorf("sanitize %q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("sanitize %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseOwnerName(t *testing.T) {
	cases := []struct {
		in, owner, name string
		ok              bool
	}{
		{"git@github.com:llbbl/dotfiles-private.git", "llbbl", "dotfiles-private", true},
		{"git@github.com:llbbl/dotfiles-private", "llbbl", "dotfiles-private", true},
		{"https://github.com/llbbl/dotfiles-private.git", "llbbl", "dotfiles-private", true},
		{"ssh://git@github.com/llbbl/dotfiles-private.git", "llbbl", "dotfiles-private", true},
	}
	for _, tc := range cases {
		o, n, ok := parseOwnerName(tc.in)
		if ok != tc.ok || o != tc.owner || n != tc.name {
			t.Errorf("parseOwnerName(%q) = %q,%q,%v want %q,%q,%v",
				tc.in, o, n, ok, tc.owner, tc.name, tc.ok)
		}
	}
}

// Silence unused warnings for diagnostic helpers.
var _ = json.Marshal
