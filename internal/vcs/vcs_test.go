package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// newBareRemote creates a bare repo to use as origin. Returns the path.
func newBareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init --bare: %v: %s", err, out)
	}
	return dir
}

func newCfg(t *testing.T, remote string) *config.Config {
	local := filepath.Join(t.TempDir(), "local")
	return &config.Config{Repo: config.RepoConfig{Remote: remote, Local: local}}
}

func TestInitLocal_WriteFile_CommitAll(t *testing.T) {
	requireGit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := newCfg(t, newBareRemote(t))
	r, err := InitLocal(ctx, cfg)
	if err != nil {
		t.Fatalf("InitLocal: %v", err)
	}
	if err := r.WriteFile("hello.txt", []byte("hi\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res, err := r.CommitAll(ctx, "first")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if res.Empty || res.SHA == "" {
		t.Errorf("expected non-empty commit, got %+v", res)
	}

	// Empty commit case.
	res2, err := r.CommitAll(ctx, "again")
	if err != nil {
		t.Fatalf("CommitAll empty: %v", err)
	}
	if !res2.Empty {
		t.Errorf("expected Empty=true on second commit, got %+v", res2)
	}
}

func TestPathEscape_Rejected(t *testing.T) {
	requireGit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := newCfg(t, newBareRemote(t))
	r, err := InitLocal(ctx, cfg)
	if err != nil {
		t.Fatalf("InitLocal: %v", err)
	}
	if err := r.WriteFile("../escape.txt", []byte("x")); err == nil {
		t.Errorf("expected ErrPathEscape")
	}
	if err := r.WriteFile("/etc/passwd", []byte("x")); err == nil {
		t.Errorf("expected ErrPathEscape for absolute")
	}
}

func TestClone_PushPull_AheadBehind(t *testing.T) {
	requireGit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	remote := newBareRemote(t)
	cfg1 := newCfg(t, remote)
	r1, err := InitLocal(ctx, cfg1)
	if err != nil {
		t.Fatalf("InitLocal: %v", err)
	}
	if err := r1.WriteFile("a.txt", []byte("one\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := r1.CommitAll(ctx, "init"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if err := r1.Push(ctx); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Second clone of the same remote.
	cfg2 := newCfg(t, remote)
	r2, err := Clone(ctx, cfg2)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r2.Path(), "a.txt")); err != nil {
		t.Errorf("expected a.txt in clone: %v", err)
	}

	// r1 commits another change; r2 should be behind after fetch.
	if err := r1.WriteFile("b.txt", []byte("two\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := r1.CommitAll(ctx, "b"); err != nil {
		t.Fatalf("CommitAll b: %v", err)
	}
	if err := r1.Push(ctx); err != nil {
		t.Fatalf("Push b: %v", err)
	}

	if err := r2.Fetch(ctx); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	ahead, behind, err := r2.AheadBehind(ctx)
	if err != nil {
		t.Fatalf("AheadBehind: %v", err)
	}
	if ahead != 0 || behind != 1 {
		t.Errorf("ahead/behind = %d/%d, want 0/1", ahead, behind)
	}

	if err := r2.PullFastForward(ctx); err != nil {
		t.Fatalf("PullFastForward: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r2.Path(), "b.txt")); err != nil {
		t.Errorf("expected b.txt after FF: %v", err)
	}
}

func TestPullKeepRemote_AndPushForce(t *testing.T) {
	requireGit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	remote := newBareRemote(t)
	cfg1 := newCfg(t, remote)
	r1, _ := InitLocal(ctx, cfg1)
	_ = r1.WriteFile("a.txt", []byte("one\n"))
	_, _ = r1.CommitAll(ctx, "init")
	_ = r1.Push(ctx)

	cfg2 := newCfg(t, remote)
	r2, err := Clone(ctx, cfg2)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// Diverge: r1 commits, r2 commits locally.
	_ = r1.WriteFile("a.txt", []byte("from-r1\n"))
	_, _ = r1.CommitAll(ctx, "r1-edit")
	_ = r1.Push(ctx)

	_ = r2.WriteFile("a.txt", []byte("from-r2\n"))
	_, _ = r2.CommitAll(ctx, "r2-edit")

	if err := r2.Fetch(ctx); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	ahead, behind, err := r2.AheadBehind(ctx)
	if err != nil {
		t.Fatalf("AheadBehind: %v", err)
	}
	if ahead == 0 || behind == 0 {
		t.Errorf("expected divergence, ahead/behind = %d/%d", ahead, behind)
	}

	if err := r2.PullKeepRemote(ctx); err != nil {
		t.Fatalf("PullKeepRemote: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(r2.Path(), "a.txt"))
	if err != nil {
		t.Fatalf("read after keep-remote: %v", err)
	}
	if strings.TrimSpace(string(got)) != "from-r1" {
		t.Errorf("after keep-remote a.txt = %q, want from-r1", got)
	}

	// Now r2 commits and force-pushes.
	_ = r2.WriteFile("a.txt", []byte("forced\n"))
	_, _ = r2.CommitAll(ctx, "forced")
	if err := r2.PushForce(ctx); err != nil {
		t.Fatalf("PushForce: %v", err)
	}
}

func TestLog_ReturnsRecentCommits(t *testing.T) {
	requireGit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := newCfg(t, newBareRemote(t))
	r, err := InitLocal(ctx, cfg)
	if err != nil {
		t.Fatalf("InitLocal: %v", err)
	}
	_ = r.WriteFile("a.txt", []byte("a\n"))
	_, _ = r.CommitAll(ctx, "one")
	_ = r.WriteFile("b.txt", []byte("b\n"))
	_, _ = r.CommitAll(ctx, "two")

	commits, err := r.Log(ctx, 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 commits, got %d", len(commits))
	}
	if commits[0].Subject != "two" || commits[1].Subject != "one" {
		t.Errorf("commits = %+v", commits)
	}
}

func TestOpen_ErrNotInitialized(t *testing.T) {
	cfg := &config.Config{Repo: config.RepoConfig{Local: t.TempDir()}}
	if _, err := Open(cfg); err == nil {
		t.Errorf("expected ErrNotInitialized")
	}
}
