// Package snapshot provides the pre-modification backup/snapshot system.
// It stores file blobs on disk (deduplicated by content hash) with metadata
// in the snapshots table, and supports listing, restore, and prune.
package snapshot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// Reason identifies WHY a snapshot was taken.
type Reason string

const (
	ReasonTrack    Reason = "track"
	ReasonManual   Reason = "manual"
	ReasonPreApply Reason = "pre-apply"
	ReasonPreSync  Reason = "pre-sync"
)

// Snapshot describes one stored backup.
type Snapshot struct {
	ID          string
	FileID      *int64
	Path        string
	Hash        string
	Size        int64
	Reason      Reason
	CreatedAt   time.Time
	StoragePath string
}

// Config is the subset of internal/config that the snapshot manager needs.
type Config struct {
	Dir           string
	MaxTotalMB    int64
	RetentionDays int
}

// Manager owns the snapshots table + the on-disk blob store.
type Manager struct {
	s   *store.Store
	cfg Config
}

// RestoreOptions controls Restore behavior.
type RestoreOptions struct {
	Overwrite bool
}

var (
	ErrSnapshotNotFound = errors.New("snapshot not found")
	ErrDestExists       = errors.New("destination exists")
	ErrBlobMissing      = errors.New("blob missing on disk")
	ErrChecksumMismatch = errors.New("blob checksum mismatch")
)

// New returns a Manager that writes blobs under cfg.Dir and metadata into
// the libSQL Store. Creates the blob root if missing.
func New(s *store.Store, cfg Config) (*Manager, error) {
	if cfg.Dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home: %w", err)
		}
		cfg.Dir = filepath.Join(home, ".local", "share", "dotfiles", "backups")
	}
	if cfg.MaxTotalMB == 0 {
		cfg.MaxTotalMB = 500
	}
	if cfg.RetentionDays == 0 {
		cfg.RetentionDays = 90
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir backup root: %w", err)
	}
	return &Manager{s: s, cfg: cfg}, nil
}

// Dir returns the configured blob root.
func (m *Manager) Dir() string { return m.cfg.Dir }

// blobPath returns the on-disk path for a given content hash.
func (m *Manager) blobPath(hash string) string {
	if len(hash) < 2 {
		return filepath.Join(m.cfg.Dir, hash)
	}
	return filepath.Join(m.cfg.Dir, hash[:2], hash)
}

// Snapshot creates one snapshot for path. file is optional.
func (m *Manager) Snapshot(ctx context.Context, path string, file *tracker.File, reason Reason) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	size := int64(len(data))

	dest := m.blobPath(hash)
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return Snapshot{}, fmt.Errorf("mkdir blob dir: %w", err)
		}
		tmp := dest + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return Snapshot{}, fmt.Errorf("write blob: %w", err)
		}
		// Verify content hash after write.
		verify, verr := os.ReadFile(tmp)
		if verr != nil {
			_ = os.Remove(tmp)
			return Snapshot{}, fmt.Errorf("verify read: %w", verr)
		}
		vsum := sha256.Sum256(verify)
		if hex.EncodeToString(vsum[:]) != hash {
			_ = os.Remove(tmp)
			return Snapshot{}, ErrChecksumMismatch
		}
		if err := os.Rename(tmp, dest); err != nil {
			_ = os.Remove(tmp)
			return Snapshot{}, fmt.Errorf("rename blob: %w", err)
		}
	} else if err != nil {
		return Snapshot{}, fmt.Errorf("stat blob: %w", err)
	}

	id, err := newID()
	if err != nil {
		return Snapshot{}, err
	}
	now := time.Now().UTC()
	createdAt := now.Format(time.RFC3339Nano)

	var fileID *int64
	if file != nil {
		fid := file.ID
		fileID = &fid
	}

	if _, err := m.s.DB().ExecContext(ctx,
		`INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, fileID, path, hash, size, string(reason), createdAt, dest,
	); err != nil {
		return Snapshot{}, fmt.Errorf("insert snapshot: %w", err)
	}

	snap := Snapshot{
		ID:          id,
		FileID:      fileID,
		Path:        path,
		Hash:        hash,
		Size:        size,
		Reason:      reason,
		CreatedAt:   now,
		StoragePath: dest,
	}
	audit.Log(ctx, "snapshot.created", map[string]any{
		"id":     id,
		"path":   path,
		"hash":   hash,
		"reason": string(reason),
		"size":   size,
	})
	return snap, nil
}

// List returns snapshots filtered by path (canonical or display). Empty
// path means all. Ordered by created_at DESC.
func (m *Manager) List(ctx context.Context, path string) ([]Snapshot, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if path == "" {
		rows, err = m.s.DB().QueryContext(ctx,
			`SELECT id, file_id, path, hash, size, reason, created_at, storage_path
			 FROM snapshots ORDER BY created_at DESC`)
	} else {
		candidates := []string{path}
		if canonical, _, rerr := tracker.Resolve(path); rerr == nil && canonical != path {
			candidates = append(candidates, canonical)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(candidates)), ",")
		args := make([]any, len(candidates))
		for i, c := range candidates {
			args[i] = c
		}
		rows, err = m.s.DB().QueryContext(ctx,
			`SELECT id, file_id, path, hash, size, reason, created_at, storage_path
			 FROM snapshots WHERE path IN (`+placeholders+`) ORDER BY created_at DESC`, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns the snapshot with the given ID.
func (m *Manager) Get(ctx context.Context, id string) (Snapshot, error) {
	row := m.s.DB().QueryRowContext(ctx,
		`SELECT id, file_id, path, hash, size, reason, created_at, storage_path
		 FROM snapshots WHERE id = ?`, id)
	s, err := scanSnapshot(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			return Snapshot{}, ErrSnapshotNotFound
		}
		return Snapshot{}, err
	}
	return s, nil
}

// Restore copies the blob bytes back to dest.
func (m *Manager) Restore(ctx context.Context, id, dest string, opts RestoreOptions) (string, int64, error) {
	snap, err := m.Get(ctx, id)
	if err != nil {
		return "", 0, err
	}
	if dest == "" {
		dest = snap.Path
	}
	if _, err := os.Stat(dest); err == nil {
		if !opts.Overwrite {
			return "", 0, ErrDestExists
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", 0, fmt.Errorf("stat dest: %w", err)
	}

	data, err := m.readBlob(snap)
	if err != nil {
		return "", 0, err
	}

	mode := os.FileMode(0o644)
	if info, ierr := os.Stat(snap.Path); ierr == nil {
		mode = info.Mode().Perm()
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir dest dir: %w", err)
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return "", 0, fmt.Errorf("write dest: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", 0, fmt.Errorf("rename dest: %w", err)
	}
	audit.Log(ctx, "snapshot.restored", map[string]any{
		"id":   id,
		"dest": dest,
		"size": int64(len(data)),
	})
	return dest, int64(len(data)), nil
}

// Open returns an io.ReadCloser over the blob bytes for the given ID.
func (m *Manager) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	snap, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(snap.StoragePath); errors.Is(err, os.ErrNotExist) {
		return nil, ErrBlobMissing
	} else if err != nil {
		return nil, err
	}
	return os.Open(snap.StoragePath)
}

// readBlob reads and verifies the blob for a snapshot.
func (m *Manager) readBlob(snap Snapshot) ([]byte, error) {
	data, err := os.ReadFile(snap.StoragePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrBlobMissing
		}
		return nil, fmt.Errorf("read blob: %w", err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != snap.Hash {
		return nil, ErrChecksumMismatch
	}
	return data, nil
}

// Prune evicts blobs based on the configured retention + size cap.
func (m *Manager) Prune(ctx context.Context) (int, int64, error) {
	return m.prune(ctx, false)
}

// PruneDryRun reports what Prune would remove without writing.
func (m *Manager) PruneDryRun(ctx context.Context) (int, int64, error) {
	return m.prune(ctx, true)
}

func (m *Manager) prune(ctx context.Context, dryRun bool) (int, int64, error) {
	all, err := m.List(ctx, "")
	if err != nil {
		return 0, 0, err
	}
	if len(all) == 0 {
		return 0, 0, nil
	}

	// "Most recent per path" key combines path + file_id (nil-safe).
	type key struct {
		path   string
		fileID int64
	}
	keep := map[string]bool{}
	latestPerKey := map[key]string{}
	for _, s := range all {
		var fid int64
		if s.FileID != nil {
			fid = *s.FileID
		}
		k := key{path: s.Path, fileID: fid}
		// `all` is sorted DESC by created_at; first seen is the newest.
		if _, ok := latestPerKey[k]; !ok {
			latestPerKey[k] = s.ID
			keep[s.ID] = true
		}
	}

	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(m.cfg.RetentionDays) * 24 * time.Hour)

	toRemove := map[string]Snapshot{}

	// Pass 1: retention.
	if m.cfg.RetentionDays > 0 {
		for _, s := range all {
			if keep[s.ID] {
				continue
			}
			if s.CreatedAt.Before(cutoff) {
				toRemove[s.ID] = s
			}
		}
	}

	// Pass 2: size cap. Build a working set of survivors, sorted oldest
	// first, and drop until under the cap.
	if m.cfg.MaxTotalMB > 0 {
		capBytes := m.cfg.MaxTotalMB * 1024 * 1024
		var survivors []Snapshot
		var total int64
		for _, s := range all {
			if _, gone := toRemove[s.ID]; gone {
				continue
			}
			survivors = append(survivors, s)
			total += s.Size
		}
		// Oldest first.
		sort.Slice(survivors, func(i, j int) bool {
			return survivors[i].CreatedAt.Before(survivors[j].CreatedAt)
		})
		for _, s := range survivors {
			if total <= capBytes {
				break
			}
			if keep[s.ID] {
				continue
			}
			toRemove[s.ID] = s
			total -= s.Size
		}
	}

	if len(toRemove) == 0 {
		return 0, 0, nil
	}

	// Count remaining refs per blob to know when to delete on-disk file.
	blobRefs := map[string]int{}
	for _, s := range all {
		blobRefs[s.StoragePath]++
	}

	var freed int64
	removed := 0
	for id, s := range toRemove {
		if !dryRun {
			if _, err := m.s.DB().ExecContext(ctx,
				`DELETE FROM snapshots WHERE id = ?`, id); err != nil {
				return removed, freed, fmt.Errorf("delete snapshot %s: %w", id, err)
			}
		}
		blobRefs[s.StoragePath]--
		if blobRefs[s.StoragePath] == 0 {
			if !dryRun {
				if err := os.Remove(s.StoragePath); err != nil && !errors.Is(err, os.ErrNotExist) {
					return removed, freed, fmt.Errorf("remove blob: %w", err)
				}
			}
			freed += s.Size
		}
		removed++
	}
	if !dryRun {
		audit.Log(ctx, "snapshot.pruned", map[string]any{
			"removed":     removed,
			"bytes_freed": freed,
		})
	}
	return removed, freed, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSnapshot(r rowScanner) (Snapshot, error) {
	var (
		id          string
		fileID      sql.NullInt64
		path        string
		hash        string
		size        int64
		reason      string
		createdAt   string
		storagePath string
	)
	if err := r.Scan(&id, &fileID, &path, &hash, &size, &reason, &createdAt, &storagePath); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{
		ID:          id,
		Path:        path,
		Hash:        hash,
		Size:        size,
		Reason:      Reason(reason),
		StoragePath: storagePath,
	}
	if fileID.Valid {
		v := fileID.Int64
		s.FileID = &v
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		s.CreatedAt = t
	} else if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		s.CreatedAt = t
	}
	return s, nil
}

// newID returns a sortable lowercase base32 ID built from a nanosecond
// timestamp + 10 bytes of crypto/rand. Roughly 26 chars, ULID-ish.
var idEncoding = base32.NewEncoding("0123456789abcdefghijklmnopqrstuv").WithPadding(base32.NoPadding)

func newID() (string, error) {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(time.Now().UnixNano()))
	if _, err := rand.Read(buf[8:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return idEncoding.EncodeToString(buf[:]), nil
}
