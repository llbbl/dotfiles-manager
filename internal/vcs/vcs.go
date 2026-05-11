// Package vcs shells out to the system git binary to manage the private
// backup repo. The parent env is NOT propagated; only a minimal allowlist
// is passed through so secrets from the surrounding process (e.g. Turso
// tokens) never leak into git subprocesses.
package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

var (
	ErrNotInitialized = errors.New("backup repo not initialized")
	ErrPathEscape     = errors.New("relative path escapes repo")
	ErrLocalNonEmpty  = errors.New("local clone target is not empty")
)

// Repo is a handle on a local clone of the backup repo.
type Repo struct {
	path   string
	origin string
}

// CommitResult describes the outcome of CommitAll.
type CommitResult struct {
	Empty  bool
	SHA    string
	Branch string
}

// Commit is one row of the log.
type Commit struct {
	SHA     string
	Author  string
	Date    time.Time
	Subject string
}

// Path returns the absolute repo path.
func (r *Repo) Path() string { return r.path }

// Open returns a Repo handle for an existing local clone.
func Open(cfg *config.Config) (*Repo, error) {
	if cfg == nil || cfg.Repo.Local == "" {
		return nil, errors.New("vcs: empty repo.local")
	}
	gitDir := filepath.Join(cfg.Repo.Local, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotInitialized
		}
		return nil, err
	}
	return &Repo{path: cfg.Repo.Local, origin: cfg.Repo.Remote}, nil
}

// Clone clones cfg.Repo.Remote into cfg.Repo.Local.
func Clone(ctx context.Context, cfg *config.Config) (*Repo, error) {
	if cfg == nil || cfg.Repo.Local == "" || cfg.Repo.Remote == "" {
		return nil, errors.New("vcs: remote and local required")
	}
	if entries, err := os.ReadDir(cfg.Repo.Local); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrLocalNonEmpty, cfg.Repo.Local)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Repo.Local), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir parent: %w", err)
	}
	if _, _, err := runGit(ctx, "", "clone", cfg.Repo.Remote, cfg.Repo.Local); err != nil {
		return nil, err
	}
	return &Repo{path: cfg.Repo.Local, origin: cfg.Repo.Remote}, nil
}

// InitLocal creates a new repo at cfg.Repo.Local with main as the default
// branch and origin set to cfg.Repo.Remote. No push.
func InitLocal(ctx context.Context, cfg *config.Config) (*Repo, error) {
	if cfg == nil || cfg.Repo.Local == "" {
		return nil, errors.New("vcs: empty repo.local")
	}
	if err := os.MkdirAll(cfg.Repo.Local, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir local: %w", err)
	}
	if _, _, err := runGit(ctx, cfg.Repo.Local, "init", "-b", "main"); err != nil {
		return nil, err
	}
	if cfg.Repo.Remote != "" {
		if _, _, err := runGit(ctx, cfg.Repo.Local, "remote", "add", "origin", cfg.Repo.Remote); err != nil {
			return nil, err
		}
	}
	r := &Repo{path: cfg.Repo.Local, origin: cfg.Repo.Remote}
	return r, nil
}

// WriteFile writes data to <repo>/<relPath>, mkdir -p as needed.
func (r *Repo) WriteFile(relPath string, data []byte) error {
	abs, err := r.safeJoin(relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(abs, data, 0o644)
}

// AppendFile appends data to <repo>/<relPath>.
func (r *Repo) AppendFile(relPath string, data []byte) error {
	abs, err := r.safeJoin(relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

func (r *Repo) safeJoin(rel string) (string, error) {
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, rel)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, rel)
	}
	return filepath.Join(r.path, cleaned), nil
}

// Add stages paths.
func (r *Repo) Add(paths ...string) error {
	args := append([]string{"add"}, paths...)
	_, _, err := runGit(context.Background(), r.path, args...)
	return err
}

// CommitAll stages all changes and creates a commit. No-op if clean.
func (r *Repo) CommitAll(ctx context.Context, message string) (CommitResult, error) {
	if _, _, err := runGit(ctx, r.path, "add", "-A"); err != nil {
		return CommitResult{}, err
	}
	out, _, err := runGit(ctx, r.path, "status", "--porcelain")
	if err != nil {
		return CommitResult{}, err
	}
	if strings.TrimSpace(out) == "" {
		branch, _ := r.currentBranch(ctx)
		return CommitResult{Empty: true, Branch: branch}, nil
	}
	args := []string{
		"-c", "user.name=dotfiles",
		"-c", "user.email=dotfiles@local",
		"commit", "-m", message,
	}
	if _, _, err := runGit(ctx, r.path, args...); err != nil {
		return CommitResult{}, err
	}
	sha, _, err := runGit(ctx, r.path, "rev-parse", "HEAD")
	if err != nil {
		return CommitResult{}, err
	}
	branch, _ := r.currentBranch(ctx)
	return CommitResult{SHA: strings.TrimSpace(sha), Branch: branch}, nil
}

func (r *Repo) currentBranch(ctx context.Context) (string, error) {
	out, _, err := runGit(ctx, r.path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Fetch fetches from origin.
func (r *Repo) Fetch(ctx context.Context) error {
	_, _, err := runGit(ctx, r.path, "fetch", "origin")
	return err
}

// Push pushes the current branch to origin.
func (r *Repo) Push(ctx context.Context) error {
	branch, err := r.currentBranch(ctx)
	if err != nil {
		return err
	}
	_, _, err = runGit(ctx, r.path, "push", "-u", "origin", branch)
	return err
}

// PullFastForward fast-forwards from origin.
func (r *Repo) PullFastForward(ctx context.Context) error {
	branch, err := r.currentBranch(ctx)
	if err != nil {
		return err
	}
	_, _, err = runGit(ctx, r.path, "merge", "--ff-only", "origin/"+branch)
	return err
}

// PullKeepRemote resets the working tree to origin/<branch>.
func (r *Repo) PullKeepRemote(ctx context.Context) error {
	branch, err := r.currentBranch(ctx)
	if err != nil {
		return err
	}
	_, _, err = runGit(ctx, r.path, "reset", "--hard", "origin/"+branch)
	return err
}

// PushForce uses --force-with-lease.
func (r *Repo) PushForce(ctx context.Context) error {
	branch, err := r.currentBranch(ctx)
	if err != nil {
		return err
	}
	_, _, err = runGit(ctx, r.path, "push", "--force-with-lease", "-u", "origin", branch)
	return err
}

// AheadBehind compares the local branch to its upstream.
func (r *Repo) AheadBehind(ctx context.Context) (int, int, error) {
	branch, err := r.currentBranch(ctx)
	if err != nil {
		return 0, 0, err
	}
	out, _, err := runGit(ctx, r.path,
		"rev-list", "--left-right", "--count", branch+"...origin/"+branch)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Fields(strings.TrimSpace(out))
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %q", out)
	}
	ahead, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse ahead: %w", err)
	}
	behind, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse behind: %w", err)
	}
	return ahead, behind, nil
}

// Log returns up to n commits (newest first).
func (r *Repo) Log(ctx context.Context, n int) ([]Commit, error) {
	if n <= 0 {
		n = 50
	}
	sep := "\x1f"
	end := "\x1e"
	format := strings.Join([]string{"%H", "%an <%ae>", "%aI", "%s"}, sep) + end
	out, _, err := runGit(ctx, r.path,
		"log", "--no-color", "-n", strconv.Itoa(n), "--pretty=format:"+format)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for line := range strings.SplitSeq(out, end) {
		line = strings.TrimLeft(line, "\n")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, sep, 4)
		if len(parts) < 4 {
			continue
		}
		c := Commit{SHA: parts[0], Author: parts[1], Subject: parts[3]}
		if t, err := time.Parse(time.RFC3339, parts[2]); err == nil {
			c.Date = t
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// runGit invokes git with a scrubbed environment. dir is the working
// directory ("" leaves it unset).
func runGit(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = scrubEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(),
			fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), stderr.String(), nil
}

// scrubEnv builds a minimal env allowlist for git subprocesses.
func scrubEnv() []string {
	keep := []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL",
		"SSH_AUTH_SOCK", "TMPDIR",
	}
	var out []string
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GIT_") {
			out = append(out, e)
		}
	}
	return out
}
