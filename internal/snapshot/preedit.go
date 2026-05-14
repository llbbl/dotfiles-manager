package snapshot

import (
	"context"
	"fmt"

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
