package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

func newTestManager(t *testing.T) (*Manager, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + filepath.Join(dir, "state.db")}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	blobDir := filepath.Join(dir, "blobs")
	mgr, err := New(s, Config{Dir: blobDir, MaxTotalMB: 500, RetentionDays: 90})
	if err != nil {
		t.Fatalf("snapshot.New: %v", err)
	}
	return mgr, s, dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSnapshot_StoresRowAndBlob(t *testing.T) {
	mgr, _, dir := newTestManager(t)
	ctx := context.Background()
	p := filepath.Join(dir, "fixture.txt")
	writeFile(t, p, "hello world\n")

	sum := sha256.Sum256([]byte("hello world\n"))
	wantHash := hex.EncodeToString(sum[:])

	snap, err := mgr.Snapshot(ctx, p, nil, ReasonManual)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Hash != wantHash {
		t.Errorf("Hash = %q, want %q", snap.Hash, wantHash)
	}
	if snap.Size != int64(len("hello world\n")) {
		t.Errorf("Size = %d", snap.Size)
	}
	if _, err := os.Stat(snap.StoragePath); err != nil {
		t.Errorf("blob missing: %v", err)
	}

	got, err := mgr.Get(ctx, snap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Hash != snap.Hash {
		t.Errorf("Get hash mismatch")
	}
}

func TestSnapshot_DeduplicatesIdenticalContent(t *testing.T) {
	mgr, _, dir := newTestManager(t)
	ctx := context.Background()
	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, "same\n")

	s1, err := mgr.Snapshot(ctx, p, nil, ReasonManual)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := mgr.Snapshot(ctx, p, nil, ReasonManual)
	if err != nil {
		t.Fatal(err)
	}
	if s1.StoragePath != s2.StoragePath {
		t.Errorf("storage paths differ: %q vs %q", s1.StoragePath, s2.StoragePath)
	}
	if s1.ID == s2.ID {
		t.Errorf("IDs should differ")
	}
	// One blob on disk.
	entries, err := os.ReadDir(filepath.Dir(s1.StoragePath))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	if count != 1 {
		t.Errorf("want 1 blob file, got %d", count)
	}
}

func TestRestore_HappyPath(t *testing.T) {
	mgr, _, dir := newTestManager(t)
	ctx := context.Background()
	p := filepath.Join(dir, "src.txt")
	writeFile(t, p, "original-content\n")

	snap, err := mgr.Snapshot(ctx, p, nil, ReasonManual)
	if err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "restored.txt")
	got, n, err := mgr.Restore(ctx, snap.ID, dest, RestoreOptions{})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got != dest {
		t.Errorf("dest = %q", got)
	}
	if n != int64(len("original-content\n")) {
		t.Errorf("n = %d", n)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original-content\n" {
		t.Errorf("data = %q", data)
	}
}

func TestRestore_DestExistsWithoutOverwrite(t *testing.T) {
	mgr, _, dir := newTestManager(t)
	ctx := context.Background()
	p := filepath.Join(dir, "src.txt")
	writeFile(t, p, "x\n")
	snap, err := mgr.Snapshot(ctx, p, nil, ReasonManual)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "blocked.txt")
	writeFile(t, dest, "existing")
	if _, _, err := mgr.Restore(ctx, snap.ID, dest, RestoreOptions{}); !errors.Is(err, ErrDestExists) {
		t.Errorf("want ErrDestExists, got %v", err)
	}
	if _, _, err := mgr.Restore(ctx, snap.ID, dest, RestoreOptions{Overwrite: true}); err != nil {
		t.Errorf("overwrite restore: %v", err)
	}
}

func TestRestore_TamperedBlobChecksumMismatch(t *testing.T) {
	mgr, _, dir := newTestManager(t)
	ctx := context.Background()
	p := filepath.Join(dir, "src.txt")
	writeFile(t, p, "trusted\n")
	snap, err := mgr.Snapshot(ctx, p, nil, ReasonManual)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper with blob bytes on disk.
	if err := os.WriteFile(snap.StoragePath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out.txt")
	if _, _, err := mgr.Restore(ctx, snap.ID, dest, RestoreOptions{}); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("want ErrChecksumMismatch, got %v", err)
	}
}

func TestList_FiltersByPath(t *testing.T) {
	mgr, _, dir := newTestManager(t)
	ctx := context.Background()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	writeFile(t, a, "aa")
	writeFile(t, b, "bb")
	if _, err := mgr.Snapshot(ctx, a, nil, ReasonManual); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Snapshot(ctx, b, nil, ReasonManual); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Snapshot(ctx, a, nil, ReasonManual); err != nil {
		t.Fatal(err)
	}

	all, _ := mgr.List(ctx, "")
	if len(all) != 3 {
		t.Errorf("all = %d", len(all))
	}
	onlyA, _ := mgr.List(ctx, a)
	if len(onlyA) != 2 {
		t.Errorf("onlyA = %d", len(onlyA))
	}
	for _, s := range onlyA {
		if s.Path != a {
			t.Errorf("wrong path: %q", s.Path)
		}
	}
}

func TestPrune_EvictsOldestRespectsLatestPerPath(t *testing.T) {
	mgr, s, dir := newTestManager(t)
	ctx := context.Background()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")

	// Tiny size cap to force eviction.
	mgr.cfg.MaxTotalMB = 0 // unlimited initially
	mgr.cfg.RetentionDays = 0

	// Create three distinct-content snapshots for a, two for b, oldest first.
	mkSnap := func(path, content string, ts time.Time) string {
		writeFile(t, path, content)
		sn, err := mgr.Snapshot(ctx, path, nil, ReasonManual)
		if err != nil {
			t.Fatal(err)
		}
		// Backdate created_at for deterministic ordering.
		if _, err := s.DB().ExecContext(ctx,
			`UPDATE snapshots SET created_at = ? WHERE id = ?`,
			ts.Format(time.RFC3339Nano), sn.ID); err != nil {
			t.Fatal(err)
		}
		return sn.ID
	}

	base := time.Now().UTC().Add(-10 * time.Hour)
	a1 := mkSnap(a, "a-v1", base)
	a2 := mkSnap(a, "a-v2", base.Add(1*time.Hour))
	a3 := mkSnap(a, "a-v3", base.Add(2*time.Hour))
	b1 := mkSnap(b, "b-v1", base.Add(3*time.Hour))
	b2 := mkSnap(b, "b-v2", base.Add(4*time.Hour))

	// Now set a tight cap: each blob is ~4 bytes. Set cap to ~0 MB but
	// use a manual cap by setting bytes via direct call. Use 0 MaxTotalMB
	// trick: we set MaxTotalMB to a value forcing eviction by computing
	// total bytes (all are 4 bytes => 20 total). Set cap to 1 byte
	// (impossible to reach exactly): we want 2 survivors (the latest
	// per path = a3 + b2).
	mgr.cfg.MaxTotalMB = 0 // skip; instead inject a custom prune via small bytes.
	// We can't set MaxTotalMB < 1 with current API meaning unlimited.
	// Trick: set RetentionDays to 1 to evict everything older than 1 day;
	// since `base` is 10h ago, no rows are older than 1d. Adjust base
	// further back so a1..b1 are older than retention while latest
	// (b2 / a3) are still within.
	// Better: backdate a1, a2, b1 to >2 days ago; keep a3, b2 recent.
	old := time.Now().UTC().Add(-72 * time.Hour)
	recent := time.Now().UTC().Add(-1 * time.Hour)
	upd := func(id string, ts time.Time) {
		if _, err := s.DB().ExecContext(ctx,
			`UPDATE snapshots SET created_at = ? WHERE id = ?`,
			ts.Format(time.RFC3339Nano), id); err != nil {
			t.Fatal(err)
		}
	}
	upd(a1, old)
	upd(a2, old.Add(time.Hour))
	upd(a3, recent)
	upd(b1, old.Add(2*time.Hour))
	upd(b2, recent.Add(time.Minute))

	mgr.cfg.RetentionDays = 1

	removed, _, err := mgr.Prune(ctx)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 3 {
		t.Errorf("removed = %d, want 3", removed)
	}

	survivors, _ := mgr.List(ctx, "")
	if len(survivors) != 2 {
		t.Fatalf("survivors = %d, want 2", len(survivors))
	}
	ids := map[string]bool{}
	for _, sn := range survivors {
		ids[sn.ID] = true
	}
	if !ids[a3] || !ids[b2] {
		t.Errorf("expected latest-per-path to survive: got %v", ids)
	}
}

func TestSnapshot_LinksFileID(t *testing.T) {
	mgr, s, dir := newTestManager(t)
	ctx := context.Background()
	p := filepath.Join(dir, "x.txt")
	writeFile(t, p, "linked\n")

	// Insert a tracked_files row manually.
	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (path, display_path, added_at, last_hash) VALUES (?, ?, ?, ?)`,
		p, p, time.Now().UTC().Format(time.RFC3339), "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	file := &tracker.File{ID: id, Path: p}

	snap, err := mgr.Snapshot(ctx, p, file, ReasonTrack)
	if err != nil {
		t.Fatal(err)
	}
	if snap.FileID == nil || *snap.FileID != id {
		t.Errorf("FileID = %v, want %d", snap.FileID, id)
	}
}
