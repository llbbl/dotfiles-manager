package snapshot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

func TestTakePreEdit_HappyPath(t *testing.T) {
	mgr, _, dir := newTestManager(t)

	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, "hello\n")

	file := tracker.File{ID: 7, Path: p, DisplayPath: "~/f.txt"}

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
	if snap.FileID == nil || *snap.FileID != 7 {
		t.Errorf("FileID = %v, want 7", snap.FileID)
	}
}

func TestTakePreEdit_WrapsErrorWithPreEditPrefix(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	file := tracker.File{ID: 1, Path: "/nonexistent/does/not/exist", DisplayPath: "x"}

	_, err := TakePreEdit(context.Background(), mgr, "/nonexistent/does/not/exist", file)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.HasPrefix(err.Error(), "pre-edit snapshot:") {
		t.Errorf("error prefix mismatch: %q", err.Error())
	}
}
