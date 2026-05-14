package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

// writeBlobFile drops a fake blob into the snapshot blob root using
// the same shard layout the snapshot manager uses (<sha[:2]>/<sha>).
// Tests use this to seed both referenced and orphan blobs without
// running the full snapshot pipeline.
func writeBlobFile(t *testing.T, root, sha, content string) string {
	t.Helper()
	dir := filepath.Join(root, sha[:2])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, sha)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

const (
	cliShaKeep = "1111111111111111111111111111111111111111111111111111111111111111"
	cliShaDrop = "2222222222222222222222222222222222222222222222222222222222222222"
)

// seedSnapshotRow records a snapshots row pointing at the given hash
// so prune --orphans knows it's referenced. Uses no tracked_files
// FK (file_id NULL) to keep the helper self-contained.
func seedSnapshotRow(t *testing.T, env *testEnv, hash, storage string) {
	t.Helper()
	_, err := env.Store.DB().ExecContext(env.Ctx,
		`INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
		 VALUES (?, NULL, ?, ?, ?, 'manual', ?, ?)`,
		"snap-"+hash[:8], "/x/y", hash, 5, "2024-01-01T00:00:00Z", storage)
	if err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
}

func TestPruneOrphansCmd_DryRunReportsButDoesNotDelete(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	keepPath := writeBlobFile(t, env.Cfg.Backup.Dir, cliShaKeep, "keepy")
	dropPath := writeBlobFile(t, env.Cfg.Backup.Dir, cliShaDrop, "byeee")
	seedSnapshotRow(t, env, cliShaKeep, keepPath)

	cmd := newPruneCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--orphans", "--dry-run", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v -- output: %s", err, out.String())
	}
	if got["orphans"].(float64) != 1 {
		t.Fatalf("orphans = %v, want 1", got["orphans"])
	}
	if got["dry_run"].(bool) != true {
		t.Fatalf("dry_run should be true")
	}
	// Files untouched.
	if _, err := os.Stat(dropPath); err != nil {
		t.Fatalf("dry-run should not delete; %s missing: %v", dropPath, err)
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("kept blob missing: %v", err)
	}
}

func TestPruneOrphansCmd_YesDeletesOrphans(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	keepPath := writeBlobFile(t, env.Cfg.Backup.Dir, cliShaKeep, "keepy")
	dropPath := writeBlobFile(t, env.Cfg.Backup.Dir, cliShaDrop, "byeee")
	seedSnapshotRow(t, env, cliShaKeep, keepPath)

	cmd := newPruneCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--orphans", "--yes", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(dropPath); !os.IsNotExist(err) {
		t.Fatalf("orphan should be deleted; err=%v", err)
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("kept blob missing: %v", err)
	}
	// Empty shard dir for dropped blob should be pruned.
	if _, err := os.Stat(filepath.Join(env.Cfg.Backup.Dir, cliShaDrop[:2])); !os.IsNotExist(err) {
		t.Fatalf("empty shard dir should be removed; err=%v", err)
	}
}

func TestPruneOrphansCmd_NoOrphans(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	keepPath := writeBlobFile(t, env.Cfg.Backup.Dir, cliShaKeep, "keepy")
	seedSnapshotRow(t, env, cliShaKeep, keepPath)

	cmd := newPruneCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--orphans", "--yes"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := out.String(); got == "" || got[:len("no orphan")] != "no orphan" {
		t.Fatalf("expected 'no orphan...' output, got: %q", got)
	}
}
