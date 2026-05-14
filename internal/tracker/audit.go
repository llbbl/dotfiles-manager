package tracker

import (
	"context"
	"fmt"
	"maps"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// RecordHashChange updates tracked_files.last_hash for file.ID to newHash
// and emits an audit event under action. The audit payload always
// includes the canonical fields display_path, file_id, snapshot_id,
// old_hash, new_hash. Any keys in extra are merged on top so per-action
// callers can add their own context (bytes_delta, sub_action, etc.).
// Keys in extra that collide with the canonical fields override them —
// callers are responsible for avoiding accidental collisions.
//
// When action is "", the UPDATE is still performed but no audit row is
// emitted. This is the apply-path mode: internal/apply does the UPDATE
// here and the cmd/dfm/apply caller emits its own audit event with a
// different field shape (suggestion_id, duration_ms, etc.) after the
// Apply call returns.
//
// The audit emit is byte-equivalent to the pre-refactor inline pattern
// used by edit, append, and alias add/remove.
func RecordHashChange(
	ctx context.Context,
	s *store.Store,
	file File,
	newHash string,
	snapID string,
	action string,
	extra map[string]any,
) error {
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE tracked_files SET last_hash = ? WHERE id = ?`, newHash, file.ID); err != nil {
		return fmt.Errorf("update tracked_files: %w", err)
	}

	if action == "" {
		return nil
	}

	fields := map[string]any{
		"display_path": file.DisplayPath,
		"file_id":      file.ID,
		"snapshot_id":  snapID,
		"old_hash":     file.LastHash,
		"new_hash":     newHash,
	}
	maps.Copy(fields, extra)

	audit.Log(ctx, action, fields)
	return nil
}
