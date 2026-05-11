package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/ids"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

func setupApplyEnv(t *testing.T) (context.Context, *store.Store, *snapshot.Manager) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		State:  config.StateConfig{URL: "file://" + filepath.Join(root, "state.db")},
		Log:    config.LogConfig{Backend: "none"},
		Backup: config.BackupConfig{Dir: filepath.Join(root, "backups")},
	}
	ctx := context.Background()
	s, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := snapshot.New(s, snapshot.Config{Dir: cfg.Backup.Dir})
	if err != nil {
		t.Fatalf("snapshot.New: %v", err)
	}
	return ctx, s, mgr
}

func insertSuggestion(t *testing.T, ctx context.Context, s *store.Store, fileID int64, diff string) string {
	t.Helper()
	id, err := ids.New()
	if err != nil {
		t.Fatalf("ids.New: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO suggestions (id, file_id, provider, prompt, diff, status, created_at)
		 VALUES (?, ?, 'fake', 'file: x\ngoal: t', ?, 'pending', ?)`,
		id, fileID, diff, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
}

func TestRepo_Apply_HappyPath(t *testing.T) {
	ctx, s, mgr := setupApplyEnv(t)

	fixDir := t.TempDir()
	fix := filepath.Join(fixDir, "fixture.txt")
	if err := os.WriteFile(fix, []byte("# fixture\nfoo=bar\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	canonical, display, err := tracker.Resolve(fix)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	file, err := tracker.Track(ctx, s, canonical, display,
		tracker.TrackOptions{SkipSecretCheck: true})
	if err != nil {
		t.Fatalf("track: %v", err)
	}

	diff := "--- a/fixture.txt\n+++ b/fixture.txt\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=baz\n"
	id := insertSuggestion(t, ctx, s, file.ID, diff)

	repo := NewRepo(s)
	res, err := repo.Apply(ctx, mgr, id)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.HunksApplied != 1 {
		t.Errorf("hunks = %d", res.HunksApplied)
	}
	if res.SnapshotID == "" {
		t.Error("snapshot id empty")
	}

	got, _ := os.ReadFile(fix)
	if string(got) != "# fixture\nfoo=baz\n" {
		t.Errorf("file = %q", got)
	}

	// Status moved to applied.
	sg, _ := repo.Get(ctx, id)
	if sg.Status != StatusApplied {
		t.Errorf("status = %q", sg.Status)
	}
	if sg.DecidedAt == nil {
		t.Error("decided_at not set")
	}

	// Re-applying must fail.
	if _, err := repo.Apply(ctx, mgr, id); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("second apply error = %v", err)
	}

	// Snapshot exists with pre-apply reason.
	snap, err := mgr.Get(ctx, res.SnapshotID)
	if err != nil {
		t.Fatalf("snapshot.Get: %v", err)
	}
	if snap.Reason != snapshot.ReasonPreApply {
		t.Errorf("reason = %q", snap.Reason)
	}
}

func TestRepo_Apply_DiffDoesNotApply_KeepsPendingAndSnapshot(t *testing.T) {
	ctx, s, mgr := setupApplyEnv(t)

	fix := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(fix, []byte("totally different\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	canonical, display, _ := tracker.Resolve(fix)
	file, err := tracker.Track(ctx, s, canonical, display,
		tracker.TrackOptions{SkipSecretCheck: true})
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	diff := "--- a/fixture.txt\n+++ b/fixture.txt\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=baz\n"
	id := insertSuggestion(t, ctx, s, file.ID, diff)

	repo := NewRepo(s)
	_, err = repo.Apply(ctx, mgr, id)
	if err == nil {
		t.Fatal("expected error")
	}
	var pse *PostSnapshotError
	if !errors.As(err, &pse) {
		t.Fatalf("want PostSnapshotError, got %v", err)
	}
	if pse.SnapshotID == "" {
		t.Error("snapshot id empty in error")
	}
	// Status stays pending.
	sg, _ := repo.Get(ctx, id)
	if sg.Status != StatusPending {
		t.Errorf("status = %q", sg.Status)
	}
	// File untouched.
	got, _ := os.ReadFile(fix)
	if string(got) != "totally different\n" {
		t.Errorf("file mutated: %q", got)
	}
}

func TestRepo_Reject(t *testing.T) {
	ctx, s, _ := setupApplyEnv(t)
	fix := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(fix, []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	canonical, display, _ := tracker.Resolve(fix)
	file, _ := tracker.Track(ctx, s, canonical, display, tracker.TrackOptions{SkipSecretCheck: true})
	id := insertSuggestion(t, ctx, s, file.ID, "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-hi\n+hello\n")

	repo := NewRepo(s)
	if err := repo.Reject(ctx, id); err != nil {
		t.Fatalf("reject: %v", err)
	}
	sg, _ := repo.Get(ctx, id)
	if sg.Status != StatusRejected {
		t.Errorf("status = %q", sg.Status)
	}
	// Second reject must fail.
	if err := repo.Reject(ctx, id); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("second reject = %v", err)
	}
}

func TestRepo_List_FiltersStatus(t *testing.T) {
	ctx, s, _ := setupApplyEnv(t)
	fix := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(fix, []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	canonical, display, _ := tracker.Resolve(fix)
	file, _ := tracker.Track(ctx, s, canonical, display, tracker.TrackOptions{SkipSecretCheck: true})

	id1 := insertSuggestion(t, ctx, s, file.ID, "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-hi\n+hello\n")
	id2 := insertSuggestion(t, ctx, s, file.ID, "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-hi\n+yo\n")

	repo := NewRepo(s)
	if err := repo.SetStatus(ctx, id1, StatusRejected); err != nil {
		t.Fatalf("set: %v", err)
	}

	pending, err := repo.List(ctx, 0, StatusPending)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id2 {
		t.Errorf("pending = %+v", pending)
	}
	all, _ := repo.List(ctx, 0, "")
	if len(all) != 2 {
		t.Errorf("all = %d, want 2", len(all))
	}
}
