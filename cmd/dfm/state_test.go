package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// newSourceDB stands up a second migrated state DB inside the
// test's tempdir so cmd/dfm tests can drive `dfm state import`
// with a real --from URL.
func newSourceDB(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "src.db")
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + path}}
	s, err := store.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("store.New src: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return "file://" + path, s
}

func TestStateImportCmd_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	srcURL, src := newSourceDB(t)
	if _, err := src.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (id, path, display_path, added_at)
		 VALUES (1, '/srcpath', '/srcpath', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed src: %v", err)
	}

	cmd := newStateCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"import", "--from", srcURL, "--tables", "tracked_files", "--yes", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v -- output: %s", err, out.String())
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v -- %s", err, out.String())
	}
	if got["imported"].(float64) != 1 {
		t.Fatalf("imported = %v, want 1", got["imported"])
	}

	var n int
	if err := env.Store.DB().QueryRow(`SELECT COUNT(*) FROM tracked_files`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("target rows = %d, want 1", n)
	}
}

func TestStateImportCmd_RefusesSameURL(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	cmd := newStateCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"import", "--from", env.Cfg.State.URL, "--yes"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error, got nil; output: %s", out.String())
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(%d), got %v", exitResolveErr, err)
	}
}

func TestStateImportCmd_DryRunNoWrites(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	srcURL, src := newSourceDB(t)
	if _, err := src.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (id, path, display_path, added_at)
		 VALUES (1, '/p', '/p', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := newStateCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"import", "--from", srcURL, "--dry-run", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got["dry_run"].(bool) != true || got["imported"].(float64) != 1 {
		t.Fatalf("unexpected result: %v", got)
	}

	var n int
	if err := env.Store.DB().QueryRow(`SELECT COUNT(*) FROM tracked_files`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("dry-run wrote %d rows", n)
	}
}

func TestStateImportCmd_MissingFromFlag(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	cmd := newStateCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"import", "--yes"})

	err := cmd.Execute()
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(%d), got %v", exitResolveErr, err)
	}
}

func TestSameStateURL(t *testing.T) {
	if !sameStateURL("file:///tmp/a", "file:///tmp/a") {
		t.Fatal("identical file URLs should match")
	}
	if sameStateURL("file:///tmp/a", "file:///tmp/b") {
		t.Fatal("different paths should not match")
	}
	if !sameStateURL("libsql://example.com?authToken=AAA", "libsql://example.com?authToken=BBB") {
		t.Fatal("same host+path with differing tokens should match")
	}
	if sameStateURL("", "file:///x") {
		t.Fatal("empty should not match")
	}
}
