package snapshot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

func TestTakePreEdit_HappyPath(t *testing.T) {
	mgr, s, dir := newTestManager(t)

	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, "hello\n")

	if _, err := s.DB().Exec(`INSERT INTO tracked_files (id, path, display_path, added_at)
		VALUES ('file-7', ?, '~/f.txt', '2024-01-01T00:00:00Z')`, p); err != nil {
		t.Fatalf("insert tracked file: %v", err)
	}
	file := tracker.File{ID: "file-7", Path: p, DisplayPath: "~/f.txt"}

	snap, err := TakePreEdit(context.Background(), mgr, p, file)
	if err != nil {
		t.Fatalf("TakePreEdit: %v", err)
	}
	if snap.ID == "" {
		t.Errorf("empty snapshot ID")
	}
	if snap.Reason != ReasonPreEdit {
		t.Errorf("reason = %q, want %q", snap.Reason, ReasonPreEdit)
	}
	if snap.FileID == nil || *snap.FileID != "file-7" {
		t.Errorf("FileID = %v, want file-7", snap.FileID)
	}
}

func TestTakePreEdit_WrapsErrorWithPreEditPrefix(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	file := tracker.File{ID: "file-1", Path: "/nonexistent/does/not/exist", DisplayPath: "x"}

	_, err := TakePreEdit(context.Background(), mgr, "/nonexistent/does/not/exist", file)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.HasPrefix(err.Error(), "pre-edit snapshot:") {
		t.Errorf("error prefix mismatch: %q", err.Error())
	}
}
