package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

func hexSum(b string) string {
	sum := sha256.Sum256([]byte(b))
	return hex.EncodeToString(sum[:])
}

// setupWriteTracked creates a tracked file at dir/sub/f.txt with mode 0600 and
// routes audit events into the test store's actions table.
func setupWriteTracked(t *testing.T) (*Manager, *store.Store, string, tracker.File) {
	t.Helper()
	mgr, s, dir := newTestManager(t)

	l, err := audit.New(context.Background(), &config.Config{Log: config.LogConfig{Backend: "db"}}, s)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	audit.SetDefault(l)
	t.Cleanup(func() { audit.SetDefault(nil) })

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sub, "f.txt")
	if err := os.WriteFile(p, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldHash := hexSum("old\n")
	if _, err := s.DB().Exec(`INSERT INTO tracked_files (id, path, display_path, added_at, last_hash)
		VALUES ('file-1', ?, '~/f.txt', '2024-01-01T00:00:00Z', ?)`, p, oldHash); err != nil {
		t.Fatalf("insert tracked file: %v", err)
	}
	return mgr, s, p, tracker.File{ID: "file-1", Path: p, DisplayPath: "~/f.txt", LastHash: oldHash}
}

func lastHash(t *testing.T, s *store.Store) string {
	t.Helper()
	var h string
	if err := s.DB().QueryRow(`SELECT last_hash FROM tracked_files WHERE id = 'file-1'`).Scan(&h); err != nil {
		t.Fatalf("select last_hash: %v", err)
	}
	return h
}

func TestWriteTracked_SnapshotsWritesAndRecords(t *testing.T) {
	mgr, s, p, file := setupWriteTracked(t)
	ctx := context.Background()

	snap, err := WriteTracked(ctx, mgr, s, p, file, []byte("new\n"), 0o640, "test.write", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("WriteTracked: %v", err)
	}

	got, err := mgr.Get(ctx, snap.ID)
	if err != nil {
		t.Fatalf("Get snapshot: %v", err)
	}
	if got.Reason != ReasonPreEdit {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonPreEdit)
	}
	if blob, err := os.ReadFile(got.StoragePath); err != nil || string(blob) != "old\n" {
		t.Errorf("snapshot blob = %q, %v; want old content", blob, err)
	}

	if b, _ := os.ReadFile(p); string(b) != "new\n" {
		t.Errorf("file content = %q, want new", b)
	}
	if info, err := os.Stat(p); err != nil {
		t.Errorf("stat: %v", err)
	} else if info.Mode().Perm() != 0o640 {
		t.Errorf("file mode = %v, want 0640", info.Mode().Perm())
	}

	newHash := hexSum("new\n")
	if h := lastHash(t, s); h != newHash {
		t.Errorf("last_hash = %q, want %q", h, newHash)
	}

	var payload string
	if err := s.DB().QueryRow(`SELECT payload_json FROM actions WHERE action = 'test.write'`).Scan(&payload); err != nil {
		t.Fatalf("select action: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	want := map[string]string{
		"snapshot_id": snap.ID,
		"old_hash":    file.LastHash,
		"new_hash":    newHash,
		"k":           "v",
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("audit %s = %v, want %q", k, fields[k], v)
		}
	}
}

func TestWriteTracked_WriteFailureLeavesHashUnchanged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	mgr, s, p, file := setupWriteTracked(t)
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := WriteTracked(context.Background(), mgr, s, p, file, []byte("new\n"), 0o600, "test.write", nil)
	if err == nil {
		t.Fatal("expected write error")
	}
	if want := "write " + p + ":"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err, want)
	}
	if h := lastHash(t, s); h != file.LastHash {
		t.Errorf("last_hash = %q, want unchanged %q", h, file.LastHash)
	}
}

func TestWriteTracked_RecordFailureReturnsErrorAfterWrite(t *testing.T) {
	mgr, s, p, file := setupWriteTracked(t)
	// Dropping tracked_files would also break the snapshot's foreign key, so only the UPDATE is made to fail.
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_update BEFORE UPDATE ON tracked_files
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, err := WriteTracked(context.Background(), mgr, s, p, file, []byte("new\n"), 0o600, "test.write", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "update tracked_files:") {
		t.Fatalf("error = %v, want unwrapped update tracked_files error", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new\n" {
		t.Errorf("file content = %q, want new", b)
	}
}
