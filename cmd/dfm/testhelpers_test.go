package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// testEnv is the shared bootstrap returned by newTestEnv. It bundles
// the per-test context, an opened store, the assembled config, and the
// filesystem paths the helper provisioned under t.TempDir(). The default
// audit logger is installed via audit.SetDefault and torn down through
// t.Cleanup — tests that need it call audit.Default() rather than
// pulling a handle off this struct.
type testEnv struct {
	Ctx   context.Context
	Store *store.Store
	Cfg   *config.Config
	Paths testPaths
}

// testPaths captures the locations newTestEnv allocates so tests can
// assert against files (e.g. the audit JSONL) without re-deriving them.
type testPaths struct {
	Root    string
	LogPath string
	StateDB string
	Backup  string
}

type envOpts struct {
	home       string
	hasHome    bool
	repoLocal  string
	repoRemote string
}

type envOpt func(*envOpts)

// WithHome overrides $HOME for the test's duration via t.Setenv.
// Mirrors what sync_test does for tests that resolve paths under HOME.
func WithHome(p string) envOpt {
	return func(o *envOpts) {
		o.home = p
		o.hasHome = true
	}
}

// WithRepoLocal sets cfg.Repo.Local — the working clone of the backup
// git repo. Only sync-style tests need this; defaults to unset.
func WithRepoLocal(p string) envOpt {
	return func(o *envOpts) { o.repoLocal = p }
}

// WithRepoRemote sets cfg.Repo.Remote — the origin URL of the backup
// git repo. Only sync-style tests need this; defaults to unset.
func WithRepoRemote(p string) envOpt {
	return func(o *envOpts) { o.repoRemote = p }
}

// newTestEnv consolidates the bootstrap that apply/suggest/sync tests
// have each been re-inlining: a tempdir-rooted state DB, audit JSONL
// path, backup dir, opened *store.Store, and a default audit.Logger
// installed via audit.SetDefault. All resources are torn down via
// t.Cleanup; callers receive a non-cancelled context.Background().
func newTestEnv(t *testing.T, opts ...envOpt) *testEnv {
	t.Helper()

	o := &envOpts{}
	for _, opt := range opts {
		opt(o)
	}

	root := t.TempDir()
	paths := testPaths{
		Root:    root,
		LogPath: filepath.Join(root, "logs", "actions.jsonl"),
		StateDB: filepath.Join(root, "state.db"),
		Backup:  filepath.Join(root, "backups"),
	}

	if o.hasHome {
		t.Setenv("HOME", o.home)
	}

	cfg := &config.Config{
		Log:    config.LogConfig{Path: paths.LogPath, Backend: "both"},
		State:  config.StateConfig{URL: "file://" + paths.StateDB},
		Backup: config.BackupConfig{Dir: paths.Backup},
		Repo:   config.RepoConfig{Remote: o.repoRemote, Local: o.repoLocal},
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

	return &testEnv{Ctx: ctx, Store: s, Cfg: cfg, Paths: paths}
}
