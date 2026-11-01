package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
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
	remote := newBareRemote(t)
	// Build $HOME under a fresh tempdir so it sits outside the env's
	// tempdir root (sync resolves tracked file paths against $HOME).
	home := t.TempDir()
	env := newTestEnv(t,
		WithHome(home),
		WithRepoRemote(remote),
		WithRepoLocal(filepath.Join(t.TempDir(), "backup-local")),
	)
	ctx, cancel := context.WithTimeout(env.Ctx, 60*time.Second)
	defer cancel()
	cfg, s, logPath, local := env.Cfg, env.Store, env.Paths.LogPath, env.Cfg.Repo.Local

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

func TestSync_CheckedOutEscapingSymlink_StopsBeforeCommitOrPush(t *testing.T) {
	requireGit(t)
	for _, linkName := range []string{"files", "logs"} {
		t.Run(linkName, func(t *testing.T) {
			remote, outside, home := newBareRemote(t), t.TempDir(), t.TempDir()
			env := newTestEnv(t, WithHome(home), WithRepoRemote(remote),
				WithRepoLocal(filepath.Join(t.TempDir(), "checkout")))
			ctx, cancel := context.WithTimeout(env.Ctx, 60*time.Second)
			defer cancel()
			seedCfg := &config.Config{Repo: config.RepoConfig{
				Local: filepath.Join(t.TempDir(), "seed"), Remote: remote,
			}}
			seed, err := vcs.InitLocal(ctx, seedCfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(seed.Path(), linkName)); err != nil {
				t.Fatal(err)
			}
			if _, err := seed.CommitAll(ctx, "fixture symlink"); err != nil {
				t.Fatal(err)
			}
			if err := seed.Push(ctx); err != nil {
				t.Fatal(err)
			}
			initialSHA := syncGit(t, remote, "rev-parse", "refs/heads/main")
			repo, err := vcs.Clone(ctx, env.Cfg)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(filepath.Join(repo.Path(), linkName))
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("checkout did not retain the fixture symlink: %v", err)
			}
			protectedName := ".zshrc"
			if linkName == "logs" {
				protectedName = "actions.jsonl"
			}
			protected := filepath.Join(outside, protectedName)
			if err := os.WriteFile(protected, []byte("outside sentinel\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".zshrc")
			if err := os.WriteFile(path, []byte("# tracked fixture\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			canonical, display, err := tracker.Resolve(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tracker.Track(ctx, env.Store, canonical, display,
				tracker.TrackOptions{SkipSecretCheck: true}); err != nil {
				t.Fatal(err)
			}
			cmd := newSyncCmd()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetContext(config.WithContext(ctx, env.Cfg))
			cmd.SetArgs([]string{})
			err = cmd.Execute()
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) {
				t.Errorf("sync error = %v, want filesystem path error", err)
			}
			if got, err := os.ReadFile(protected); err != nil || string(got) != "outside sentinel\n" {
				t.Errorf("sync modified outside target: %q, %v", got, err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 {
				t.Errorf("sync created outside files: %v, %v", entries, err)
			}
			if syncGit(t, remote, "rev-parse", "refs/heads/main") != initialSHA {
				t.Error("sync pushed despite escaping backup path")
			}
			if syncGit(t, repo.Path(), "rev-parse", "HEAD") != initialSHA {
				t.Error("sync committed despite escaping backup path")
			}
		})
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

func TestSync_FailedPush_UnchangedRetryPushesPendingCommits(t *testing.T) {
	requireGit(t)
	for _, upstream := range []bool{false, true} {
		for _, asJSON := range []bool{false, true} {
			name := "first-push"
			if upstream {
				name = "existing-origin"
			}
			if asJSON {
				name += "-json"
			}
			t.Run(name, func(t *testing.T) {
				remote := newBareRemote(t)
				home := t.TempDir()
				env := newTestEnv(t, WithHome(home), WithRepoRemote(remote),
					WithRepoLocal(filepath.Join(t.TempDir(), "backup")))
				ctx, cancel := context.WithTimeout(env.Ctx, 60*time.Second)
				defer cancel()
				repo, err := vcs.InitLocal(ctx, env.Cfg)
				if err != nil {
					t.Fatal(err)
				}
				if upstream {
					if err := repo.WriteFile("README.md", []byte("# backup\n")); err != nil {
						t.Fatal(err)
					}
					if _, err := repo.CommitAll(ctx, "init"); err != nil {
						t.Fatal(err)
					}
					if err := repo.Push(ctx); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(home, ".zshrc")
				if err := os.WriteFile(path, []byte("# fixture\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				canonical, display, err := tracker.Resolve(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tracker.Track(ctx, env.Store, canonical, display,
					tracker.TrackOptions{SkipSecretCheck: true}); err != nil {
					t.Fatal(err)
				}

				rejectHook := filepath.Join(remote, "hooks", "pre-receive")
				if err := os.WriteFile(rejectHook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				run := func(args ...string) (string, error) {
					t.Helper()
					cmd := newSyncCmd()
					var out, stderr bytes.Buffer
					cmd.SetOut(&out)
					cmd.SetErr(&stderr)
					cmd.SetContext(config.WithContext(ctx, env.Cfg))
					cmd.SetArgs(args)
					err := cmd.Execute()
					return out.String(), err
				}
				if _, err := run(); err == nil {
					t.Fatal("expected rejected push")
				}
				pendingSHA := syncGit(t, repo.Path(), "rev-parse", "HEAD")
				if status := syncGit(t, repo.Path(), "status", "--porcelain"); status != "" {
					t.Fatalf("failed push left uncommitted files: %q", status)
				}
				assertRemotePending := func() {
					t.Helper()
					cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/main")
					cmd.Dir = remote
					out, err := cmd.Output()
					if upstream && err != nil {
						t.Fatalf("read remote ref: %v", err)
					}
					if !upstream && err == nil {
						t.Fatal("failed first push created a remote branch")
					}
					if strings.TrimSpace(string(out)) == pendingSHA {
						t.Fatal("pending commit unexpectedly reached remote")
					}
				}
				assertRemotePending()

				var beforeSnapshots int
				if err := env.Store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshots").Scan(&beforeSnapshots); err != nil {
					t.Fatal(err)
				}
				if _, err := run("--dry-run", "--json"); err != nil {
					t.Fatalf("dry-run: %v", err)
				}
				assertRemotePending()
				var afterSnapshots int
				if err := env.Store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshots").Scan(&afterSnapshots); err != nil {
					t.Fatal(err)
				}
				if afterSnapshots != beforeSnapshots || syncGit(t, repo.Path(), "rev-parse", "HEAD") != pendingSHA ||
					syncGit(t, repo.Path(), "status", "--porcelain") != "" {
					t.Fatal("dry-run changed local state")
				}

				if err := os.Remove(rejectHook); err != nil {
					t.Fatal(err)
				}
				var args []string
				if asJSON {
					args = []string{"--json"}
				}
				out, err := run(args...)
				if err != nil {
					t.Fatalf("retry: %v", err)
				}
				if remoteSHA := syncGit(t, remote, "rev-parse", "refs/heads/main"); remoteSHA != pendingSHA {
					t.Fatalf("remote ref = %s, want pending commit %s", remoteSHA, pendingSHA)
				}
				if localSHA := syncGit(t, repo.Path(), "rev-parse", "HEAD"); localSHA != pendingSHA {
					t.Fatal("unchanged retry created a new commit")
				}
				if asJSON {
					assertSyncSummary(t, out, true)
				} else if !strings.Contains(out, "pushed pending commits to origin/main (no new commit)") ||
					strings.Contains(out, "no changes") {
					t.Fatalf("retry output = %q", out)
				}

				// A clean, fully synced run must not attempt another push.
				prePush := filepath.Join(repo.Path(), ".git", "hooks", "pre-push")
				if err := os.WriteFile(prePush, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				out, err = run("--json")
				if err != nil {
					t.Fatalf("clean no-op: %v", err)
				}
				assertSyncSummary(t, out, false)
			})
		}
	}
}

func syncGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSync_KeepLocalReportsSuccessfulPush(t *testing.T) {
	requireGit(t)
	remote := newBareRemote(t)
	env := newTestEnv(t, WithRepoRemote(remote),
		WithRepoLocal(filepath.Join(t.TempDir(), "backup")))
	ctx, cancel := context.WithTimeout(env.Ctx, 60*time.Second)
	defer cancel()
	repo, err := vcs.InitLocal(ctx, env.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteFile("README.md", []byte("# initial\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitAll(ctx, "init"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(ctx); err != nil {
		t.Fatal(err)
	}
	otherCfg := &config.Config{Repo: config.RepoConfig{
		Remote: remote, Local: filepath.Join(t.TempDir(), "other"),
	}}
	other, err := vcs.Clone(ctx, otherCfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*vcs.Repo{repo, other} {
		if err := r.WriteFile("README.md", []byte(r.Path()+"\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CommitAll(ctx, "diverge"); err != nil {
			t.Fatal(err)
		}
	}
	if err := other.Push(ctx); err != nil {
		t.Fatal(err)
	}
	cmd := newSyncCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetContext(config.WithContext(ctx, env.Cfg))
	cmd.SetArgs([]string{"--strategy=keep-local", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	assertSyncSummary(t, out.String(), true)
	if syncGit(t, remote, "rev-parse", "refs/heads/main") != syncGit(t, repo.Path(), "rev-parse", "HEAD") {
		t.Fatal("keep-local did not push the local branch")
	}
}

func assertSyncSummary(t *testing.T, out string, pushed bool) {
	t.Helper()
	var summary struct {
		Empty   bool   `json:"empty"`
		Pushed  bool   `json:"pushed"`
		Changes int    `json:"changes"`
		Commit  string `json:"commit"`
		Branch  string `json:"branch"`
	}
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if !summary.Empty || summary.Pushed != pushed || summary.Changes != 0 || summary.Commit != "" || summary.Branch != "main" {
		t.Fatalf("summary = %+v, want empty=true pushed=%v changes=0 commit=empty branch=main", summary, pushed)
	}
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
