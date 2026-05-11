// Package apply manages the suggestions lifecycle: lookup, render,
// apply (with pre-write snapshot), and reject. The in-process unified
// diff applier lives in diff.go.
package apply

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// Status values for the suggestions.status column.
const (
	StatusPending  = "pending"
	StatusApplied  = "applied"
	StatusRejected = "rejected"
	StatusStale    = "stale"
)

// Errors.
var (
	ErrNotFound         = errors.New("suggestion not found")
	ErrAlreadyDecided   = errors.New("suggestion already decided")
	ErrFileMissing      = errors.New("suggestion file is no longer tracked")
	ErrDiffEmpty        = errors.New("diff is empty")
	ErrDiffMalformed    = errors.New("diff is malformed")
	ErrDiffDoesNotApply = errors.New("diff does not apply")
)

// Suggestion is a row from the suggestions table, decoded.
type Suggestion struct {
	ID        string
	FileID    int64
	Provider  string
	Prompt    string
	Diff      string
	Status    string
	CreatedAt time.Time
	DecidedAt *time.Time
}

// ApplyResult is the success-case payload from Apply.
type ApplyResult struct {
	NewHash      string `json:"new_hash"`
	SnapshotID   string `json:"snapshot_id"`
	HunksApplied int    `json:"hunks_applied"`
}

// PostSnapshotError is returned when a failure occurs after the
// pre-apply snapshot was taken. The user can recover via
// `dotfiles restore <SnapshotID>`. Status of the suggestion stays
// "pending" so retries remain possible.
type PostSnapshotError struct {
	SnapshotID string
	Err        error
}

func (e *PostSnapshotError) Error() string {
	return fmt.Sprintf("%v (recover with: dotfiles restore %s)", e.Err, e.SnapshotID)
}

func (e *PostSnapshotError) Unwrap() error { return e.Err }

// Repo provides CRUD against the suggestions table plus the Apply +
// Reject workflows.
type Repo struct {
	s *store.Store
}

// NewRepo binds the repo to an open Store.
func NewRepo(s *store.Store) *Repo { return &Repo{s: s} }

// Get returns the suggestion with the given id.
func (r *Repo) Get(ctx context.Context, id string) (Suggestion, error) {
	row := r.s.DB().QueryRowContext(ctx, `
		SELECT id, COALESCE(file_id, 0), provider, prompt, diff, status, created_at, decided_at
		FROM suggestions WHERE id = ?`, id)
	return scanSuggestion(row)
}

// List returns suggestions ordered by created_at DESC. Pass fileID=0 to
// skip the file filter and status="" to skip the status filter.
func (r *Repo) List(ctx context.Context, fileID int64, status string) ([]Suggestion, error) {
	q := `SELECT id, COALESCE(file_id, 0), provider, prompt, diff, status, created_at, decided_at
	      FROM suggestions`
	var conds []string
	var args []any
	if fileID > 0 {
		conds = append(conds, "file_id = ?")
		args = append(args, fileID)
	}
	if status != "" {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY created_at DESC"
	rows, err := r.s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query suggestions: %w", err)
	}
	defer rows.Close()
	var out []Suggestion
	for rows.Next() {
		sg, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// SetStatus moves a pending suggestion to a decided status.
func (r *Repo) SetStatus(ctx context.Context, id, status string) error {
	sg, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if sg.Status != StatusPending {
		return ErrAlreadyDecided
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.s.DB().ExecContext(ctx,
		`UPDATE suggestions SET status = ?, decided_at = ? WHERE id = ? AND status = 'pending'`,
		status, now, id); err != nil {
		return fmt.Errorf("update suggestion status: %w", err)
	}
	return nil
}

// ResolveFile returns the tracker.File referenced by the suggestion.
func (r *Repo) ResolveFile(ctx context.Context, sugID string) (tracker.File, error) {
	row := r.s.DB().QueryRowContext(ctx, `SELECT file_id FROM suggestions WHERE id = ?`, sugID)
	var fileID sql.NullInt64
	if err := row.Scan(&fileID); err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			return tracker.File{}, ErrNotFound
		}
		return tracker.File{}, err
	}
	if !fileID.Valid || fileID.Int64 == 0 {
		return tracker.File{}, ErrFileMissing
	}
	frow := r.s.DB().QueryRowContext(ctx, `
		SELECT id, path, display_path, added_at, COALESCE(last_hash,''), COALESCE(last_synced,'')
		FROM tracked_files WHERE id = ?`, fileID.Int64)
	var (
		id          int64
		path        string
		display     string
		addedAt     string
		lastHash    string
		lastSynced  string
	)
	if err := frow.Scan(&id, &path, &display, &addedAt, &lastHash, &lastSynced); err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			return tracker.File{}, ErrFileMissing
		}
		return tracker.File{}, err
	}
	f := tracker.File{ID: id, Path: path, DisplayPath: display, LastHash: lastHash}
	if t, err := time.Parse(time.RFC3339, addedAt); err == nil {
		f.AddedAt = t
	}
	if lastSynced != "" {
		if t, err := time.Parse(time.RFC3339, lastSynced); err == nil {
			f.LastSynced = t
		}
	}
	return f, nil
}

// Apply runs the full apply workflow. See package docs for the steps.
// On any failure after the pre-apply snapshot is taken the suggestion
// stays "pending" and the returned error carries the snapshot id.
func (r *Repo) Apply(ctx context.Context, mgr *snapshot.Manager, id string) (ApplyResult, error) {
	sg, err := r.Get(ctx, id)
	if err != nil {
		return ApplyResult{}, err
	}
	if sg.Status != StatusPending {
		return ApplyResult{}, ErrAlreadyDecided
	}
	file, err := r.ResolveFile(ctx, id)
	if err != nil {
		return ApplyResult{}, err
	}

	orig, err := os.ReadFile(file.Path)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("read %s: %w", file.Path, err)
	}

	fs, err := Validate(sg.Diff)
	if err != nil {
		return ApplyResult{}, err
	}
	if !diffTargets(fs, file) {
		return ApplyResult{}, fmt.Errorf("%w: diff target mismatch", ErrDiffMalformed)
	}

	// Snapshot BEFORE any write.
	f := file
	snap, err := mgr.Snapshot(ctx, file.Path, &f, snapshot.ReasonPreApply)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("pre-apply snapshot: %w", err)
	}

	newBytes, hunks, err := ApplyToBytes(orig, fs)
	if err != nil {
		// Validation failed; do NOT touch the file on disk. Surface the
		// snapshot id so the user has a paper trail.
		return ApplyResult{}, &PostSnapshotError{SnapshotID: snap.ID, Err: err}
	}

	if err := atomicWrite(file.Path, newBytes); err != nil {
		return ApplyResult{}, &PostSnapshotError{SnapshotID: snap.ID, Err: err}
	}

	sum := sha256.Sum256(newBytes)
	newHash := hex.EncodeToString(sum[:])

	if _, err := r.s.DB().ExecContext(ctx,
		`UPDATE tracked_files SET last_hash = ? WHERE id = ?`, newHash, file.ID); err != nil {
		return ApplyResult{}, &PostSnapshotError{SnapshotID: snap.ID,
			Err: fmt.Errorf("update tracked_files: %w", err)}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.s.DB().ExecContext(ctx,
		`UPDATE suggestions SET status = 'applied', decided_at = ?
		 WHERE id = ? AND status = 'pending'`, now, id); err != nil {
		return ApplyResult{}, &PostSnapshotError{SnapshotID: snap.ID,
			Err: fmt.Errorf("update suggestion status: %w", err)}
	}

	return ApplyResult{
		NewHash:      newHash,
		SnapshotID:   snap.ID,
		HunksApplied: hunks,
	}, nil
}

// Reject marks a pending suggestion as rejected. No file is touched.
func (r *Repo) Reject(ctx context.Context, id string) error {
	return r.SetStatus(ctx, id, StatusRejected)
}

// diffTargets compares the diff headers against the suggestion's
// resolved file path. We accept any of: canonical path, display path,
// the basename, or one of those with an `a/` / `b/` prefix.
func diffTargets(fs FileSet, f tracker.File) bool {
	candidates := []string{
		f.Path, f.DisplayPath, filepath.Base(f.Path),
	}
	expect := map[string]bool{}
	for _, c := range candidates {
		expect[c] = true
		expect["a/"+c] = true
		expect["b/"+c] = true
		// display paths like "~/x/y": strip tilde
		if rest, ok := strings.CutPrefix(c, "~/"); ok {
			expect[rest] = true
			expect["a/"+rest] = true
			expect["b/"+rest] = true
		}
	}
	// /dev/null is acceptable on either side for create/delete diffs;
	// out of scope here, but allow it to not break.
	expect["/dev/null"] = true
	return expect[fs.OldPath] && expect[fs.NewPath]
}

// atomicWrite writes data to path via a tmp file + rename, preserving
// the existing mode bits when the path is already present.
func atomicWrite(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dotfiles-apply-*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSuggestion(r rowScanner) (Suggestion, error) {
	var (
		id        string
		fileID    int64
		provider  string
		prompt    string
		diff      string
		status    string
		createdAt string
		decidedAt sql.NullString
	)
	if err := r.Scan(&id, &fileID, &provider, &prompt, &diff, &status, &createdAt, &decidedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			return Suggestion{}, ErrNotFound
		}
		return Suggestion{}, fmt.Errorf("scan suggestion: %w", err)
	}
	sg := Suggestion{
		ID:       id,
		FileID:   fileID,
		Provider: provider,
		Prompt:   prompt,
		Diff:     diff,
		Status:   status,
	}
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		sg.CreatedAt = t
	}
	if decidedAt.Valid && decidedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, decidedAt.String); err == nil {
			sg.DecidedAt = &t
		}
	}
	return sg, nil
}
