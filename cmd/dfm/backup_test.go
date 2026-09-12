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

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/spf13/cobra"
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

// firstTableID returns the ID column of the first data row of a rendered
// table, i.e. the truncated id a user would copy off their terminal.
func firstTableID(t *testing.T, table string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(table), "\n")
	if len(lines) < 2 {
		t.Fatalf("table has no data rows:\n%s", table)
	}
	return strings.Fields(lines[1])[0]
}

func runCmd(t *testing.T, cmd *cobra.Command, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	cmd.SetContext(ctx)
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errBuf.String(), err
}

// TestRestoreCmd_AcceptsIDCopiedFromBackupsTable is the regression for the
// original report: the listing truncates ids to 10 chars, so restore must
// accept that prefix.
func TestRestoreCmd_AcceptsIDCopiedFromBackupsTable(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	src := filepath.Join(t.TempDir(), "conf.txt")
	if err := os.WriteFile(src, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	backupOut, _, err := runCmd(t, newBackupCmd(), ctx, "--json", src)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(backupOut), &created); err != nil {
		t.Fatalf("backup json: %v -- %s", err, backupOut)
	}
	fullID := created["id"].(string)

	listOut, _, err := runCmd(t, newBackupsCmd(), ctx)
	if err != nil {
		t.Fatalf("backups: %v", err)
	}
	shortID := firstTableID(t, listOut)
	if len(shortID) != 10 || !strings.HasPrefix(fullID, shortID) {
		t.Fatalf("short id %q is not a 10-char prefix of %q", shortID, fullID)
	}

	dest := filepath.Join(t.TempDir(), "restored.txt")
	restoreOut, _, err := runCmd(t, newRestoreCmd(), ctx, "--json", "--to", dest, shortID)
	if err != nil {
		t.Fatalf("restore with truncated id: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(restoreOut), &res); err != nil {
		t.Fatalf("restore json: %v -- %s", err, restoreOut)
	}
	if res["id"] != fullID {
		t.Errorf("json id = %v, want the resolved full id %s", res["id"], fullID)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "hello\n" {
		t.Errorf("restored content = %q", got)
	}
}

func TestRestoreCmd_AmbiguousPrefixListsCandidates(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	const (
		idA = "01HQAAAAAAAAAAAAAAAAAAAAAA"
		idB = "01HQAAAAAABBBBBBBBBBBBBBBB"
	)
	for _, id := range []string{idA, idB} {
		if _, err := env.Store.DB().ExecContext(env.Ctx,
			`INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
			 VALUES (?, NULL, '/x/y', ?, 5, 'manual', '2024-01-01T00:00:00Z', '/blob')`,
			id, cliShaKeep); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	_, stderr, err := runCmd(t, newRestoreCmd(), ctx, idA[:10])
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitAmbiguousID {
		t.Errorf("exit = %d, want %d", ee.code, exitAmbiguousID)
	}
	for _, id := range []string{idA, idB} {
		if !strings.Contains(stderr, id) {
			t.Errorf("stderr missing candidate %s:\n%s", id, stderr)
		}
	}
}

func TestRestoreCmd_UnknownPrefixSaysPrefixMatchedNothing(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	_, _, err := runCmd(t, newRestoreCmd(), ctx, "NOPE")
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitAlreadyOrMiss {
		t.Errorf("exit = %d, want %d", ee.code, exitAlreadyOrMiss)
	}
	if !strings.Contains(ee.msg, "no snapshot matches id prefix") {
		t.Errorf("msg = %q", ee.msg)
	}
}
