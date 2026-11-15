package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/llbbl/dotfiles-manager/internal/fsx"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// TakePreEdit captures a ReasonPreEdit snapshot of canonical for file
// using mgr. It exists so the cmd/dfm mutation commands (edit, append,
// alias add, alias remove) don't have to repeat the "f := file" address-
// of dance plus the standard "pre-edit snapshot: %w" error wrap.
//
// Callers continue to construct mgr themselves (typically via
// newSnapshotManager in cmd/dfm) and wrap any construction error with
// the canonical "snapshot manager: %w" prefix.
//
// This helper lives in the snapshot package (not internal/tracker) to
// avoid an import cycle: snapshot already imports tracker.
func TakePreEdit(ctx context.Context, mgr *Manager, canonical string, file tracker.File) (Snapshot, error) {
	f := file
	snap, err := mgr.Snapshot(ctx, canonical, &f, ReasonPreEdit)
	if err != nil {
		return Snapshot{}, fmt.Errorf("pre-edit snapshot: %w", err)
	}
	return snap, nil
}

// WriteTracked takes a pre-edit snapshot of canonical, atomically writes
// content with mode, and records the new hash and audit event for file.
func WriteTracked(ctx context.Context, mgr *Manager, s *store.Store, canonical string, file tracker.File,
	content []byte, mode os.FileMode, action string, meta map[string]any) (Snapshot, error) {
	snap, err := TakePreEdit(ctx, mgr, canonical, file)
	if err != nil {
		return Snapshot{}, err
	}
	if err := fsx.AtomicWrite(canonical, content, mode); err != nil {
		return Snapshot{}, fmt.Errorf("write %s: %w", canonical, err)
	}
	sum := sha256.Sum256(content)
	if err := tracker.RecordHashChange(ctx, s, file, hex.EncodeToString(sum[:]), snap.ID, action, meta); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}
