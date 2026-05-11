// Package tracker provides path resolution and CRUD operations on the
// tracked_files table, plus per-file status comparison.
package tracker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// File is one row of the tracked_files table.
type File struct {
	ID          int64
	Path        string
	DisplayPath string
	AddedAt     time.Time
	LastHash    string
	LastSynced  time.Time
}

// Status is the comparison result between a tracked file's recorded
// hash and its current on-disk contents.
type Status string

// Status values reported by ComputeStatus.
const (
	StatusClean    Status = "clean"
	StatusModified Status = "modified"
	StatusMissing  Status = "missing"
	StatusNew      Status = "new"
)

// StatusReport pairs a tracked File with its current Status and (when
// applicable) the freshly-computed content hash.
type StatusReport struct {
	File   File
	Status Status
	Hash   string
}

// TrackOptions tunes Track's behavior.
type TrackOptions struct {
	SkipSecretCheck bool
	Reset           bool
	// AfterCommit is invoked after the tracked_files row is inserted or
	// updated, with the resulting File. Errors are surfaced via the
	// returned (File, error) pair to the caller; if non-nil, the row is
	// still committed. Used by the snapshot subsystem to take a
	// ReasonTrack snapshot.
	AfterCommit func(ctx context.Context, f File) error
}

// Sentinel errors returned by Track/Untrack/Resolve.
var (
	ErrAlreadyTracked     = errors.New("path is already tracked")
	ErrNotTracked         = errors.New("path is not tracked")
	ErrIsDirectory        = errors.New("path is a directory")
	ErrPathOutsideAllowed = errors.New("path is outside the allowed roots")
)

// SecretsError wraps a secrets scan result so the CLI can render the
// findings table. Returned by Track when the pre-flight scan flags the
// file.
type SecretsError struct {
	Result secrets.Result
	Path   string
}

// Error implements the error interface.
func (e *SecretsError) Error() string {
	return fmt.Sprintf("secrets detected in %s (%d findings)", e.Path, len(e.Result.Findings))
}

// BinarySuffixError is returned when a path matches a binary-ish suffix
// and TrackOptions.SkipSecretCheck (the --force flag) is not set.
type BinarySuffixError struct {
	Path   string
	Suffix string
}

// Error implements the error interface.
func (e *BinarySuffixError) Error() string {
	return fmt.Sprintf("refusing %s: suspicious suffix %q (use --force to override)", e.Path, e.Suffix)
}

// systemRoots are filesystem roots we refuse to track from regardless of
// the user's request. Anything that canonicalizes under one of these is
// rejected before any DB write.
var systemRoots = []string{
	"/etc",
	"/usr",
	"/var",
	"/private/etc",
	"/private/var",
	"/System",
	"/Library",
}

// binarySuffixes are suffixes that almost certainly shouldn't live in a
// dotfile-management repo; flagged as a soft refusal unless --force.
var binarySuffixes = []string{
	".so", ".dylib", ".dll", ".exe", ".bin", ".o", ".a",
}

// Resolve takes a raw user-supplied path and produces a canonical absolute
// path plus a display path. It also enforces path policy (no dirs, no
// system roots, no escapes via symlink).
func Resolve(raw string) (canonical, display string, err error) {
	if strings.TrimSpace(raw) == "" {
		return "", "", errors.New("empty path")
	}

	expanded, err := expandHome(raw)
	if err != nil {
		return "", "", err
	}

	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", "", fmt.Errorf("resolve %s: %w", raw, err)
	}
	abs = filepath.Clean(abs)

	info, err := os.Lstat(abs)
	if err != nil {
		return "", "", fmt.Errorf("stat %s: %w", abs, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("%w: %s", ErrIsDirectory, abs)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", fmt.Errorf("eval symlinks for %s: %w", abs, err)
	}
	resolved = filepath.Clean(resolved)

	finfo, err := os.Stat(resolved)
	if err != nil {
		return "", "", fmt.Errorf("stat resolved %s: %w", resolved, err)
	}
	if finfo.IsDir() {
		return "", "", fmt.Errorf("%w: %s", ErrIsDirectory, resolved)
	}

	if isUnderSystemRoot(abs) || isUnderSystemRoot(resolved) {
		return "", "", fmt.Errorf("%w: %s is under a system root", ErrPathOutsideAllowed, resolved)
	}

	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	if home != "" {
		if rhome, err := filepath.EvalSymlinks(home); err == nil {
			home = rhome
		}
	}
	if cwd != "" {
		if rcwd, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = rcwd
		}
	}

	allowedTmps := tmpRoots()
	allowed := isUnder(resolved, home) || isUnder(resolved, cwd)
	for _, t := range allowedTmps {
		if isUnder(resolved, t) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", "", fmt.Errorf("%w: %s is not under $HOME, cwd, or tmp", ErrPathOutsideAllowed, resolved)
	}

	display = resolved
	if home != "" && isUnder(resolved, home) {
		rel, err := filepath.Rel(home, resolved)
		if err == nil {
			display = "~/" + filepath.ToSlash(rel)
		}
	}

	return resolved, display, nil
}

func isUnderSystemRoot(p string) bool {
	// Exempt OS temp roots; on macOS the user temp dir lives under
	// /var/folders, which would otherwise be caught by the /var entry.
	for _, t := range tmpRoots() {
		if isUnder(p, t) {
			return false
		}
	}
	for _, r := range systemRoots {
		if isUnder(p, r) {
			return true
		}
	}
	return false
}

func tmpRoots() []string {
	roots := []string{"/tmp", "/private/tmp"}
	if t := os.TempDir(); t != "" {
		roots = append(roots, t)
		if r, err := filepath.EvalSymlinks(t); err == nil {
			roots = append(roots, r)
		}
	}
	return roots
}

func isUnder(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if strings.HasPrefix(rel, "..") {
		return false
	}
	return !filepath.IsAbs(rel)
}

func expandHome(p string) (string, error) {
	if p == "~" {
		return os.UserHomeDir()
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// HasBinarySuffix reports whether the path ends in a binary-ish extension
// (caller decides whether to refuse).
func HasBinarySuffix(p string) (string, bool) {
	lower := strings.ToLower(p)
	for _, s := range binarySuffixes {
		if strings.HasSuffix(lower, s) {
			return s, true
		}
	}
	return "", false
}

// HashFile streams the file and returns lowercase hex SHA-256.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Track inserts a new tracked_files row (or refreshes one when opts.Reset).
func Track(ctx context.Context, s *store.Store, canonical, display string, opts TrackOptions) (File, error) {
	if !opts.SkipSecretCheck {
		res, err := secrets.ScanFile(canonical)
		if err != nil {
			return File{}, fmt.Errorf("secret scan: %w", err)
		}
		if !res.Skipped && len(res.Findings) > 0 {
			return File{}, &SecretsError{Result: res, Path: canonical}
		}
	}

	hash, err := HashFile(canonical)
	if err != nil {
		return File{}, err
	}

	now := time.Now().UTC()
	addedAt := now.Format(time.RFC3339)

	dlog.From(ctx).Debug("tracking file", "display", display, "hash", shortHash(hash))

	existing, err := findByPath(ctx, s, canonical)
	if err != nil {
		return File{}, err
	}
	if existing != nil {
		if !opts.Reset {
			return *existing, ErrAlreadyTracked
		}
		_, err := s.DB().ExecContext(ctx,
			`UPDATE tracked_files SET last_hash = ?, added_at = ?, display_path = ? WHERE id = ?`,
			hash, addedAt, display, existing.ID)
		if err != nil {
			return File{}, fmt.Errorf("update tracked_files: %w", err)
		}
		existing.LastHash = hash
		existing.AddedAt = now
		existing.DisplayPath = display
		f := *existing
		var cbErr error
		if opts.AfterCommit != nil {
			cbErr = opts.AfterCommit(ctx, f)
		}
		return f, cbErr
	}

	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (path, display_path, added_at, last_hash) VALUES (?, ?, ?, ?)`,
		canonical, display, addedAt, hash)
	if err != nil {
		return File{}, fmt.Errorf("insert tracked_files: %w", err)
	}
	id, _ := res.LastInsertId()
	f := File{
		ID:          id,
		Path:        canonical,
		DisplayPath: display,
		AddedAt:     now,
		LastHash:    hash,
	}
	var cbErr error
	if opts.AfterCommit != nil {
		cbErr = opts.AfterCommit(ctx, f)
	}
	return f, cbErr
}

// Untrack removes a tracked_files row by canonical, display, or relative
// path.
func Untrack(ctx context.Context, s *store.Store, target string) (File, error) {
	f, err := findByAnyForm(ctx, s, target)
	if err != nil {
		return File{}, err
	}
	if f == nil {
		return File{}, ErrNotTracked
	}
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM tracked_files WHERE id = ?`, f.ID); err != nil {
		return File{}, fmt.Errorf("delete tracked_files: %w", err)
	}
	return *f, nil
}

// List returns all tracked files ordered by display_path.
func List(ctx context.Context, s *store.Store) ([]File, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT id, path, display_path, added_at, COALESCE(last_hash,''), COALESCE(last_synced,'')
		 FROM tracked_files ORDER BY display_path ASC`)
	if err != nil {
		return nil, fmt.Errorf("query tracked_files: %w", err)
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ComputeStatus reports the status for every tracked file.
func ComputeStatus(ctx context.Context, s *store.Store) ([]StatusReport, error) {
	start := time.Now()
	files, err := List(ctx, s)
	if err != nil {
		return nil, err
	}
	out := make([]StatusReport, 0, len(files))
	for _, f := range files {
		out = append(out, statusFor(f))
	}
	dlog.From(ctx).Debug("status pass",
		"count", len(files),
		"duration_ms", time.Since(start).Milliseconds())
	return out, nil
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// ComputeStatusOne returns the status for a single tracked file, matched
// by canonical, display, or relative form.
func ComputeStatusOne(ctx context.Context, s *store.Store, target string) (StatusReport, error) {
	f, err := findByAnyForm(ctx, s, target)
	if err != nil {
		return StatusReport{}, err
	}
	if f == nil {
		return StatusReport{}, ErrNotTracked
	}
	return statusFor(*f), nil
}

func statusFor(f File) StatusReport {
	info, err := os.Stat(f.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StatusReport{File: f, Status: StatusMissing}
		}
		return StatusReport{File: f, Status: StatusMissing}
	}
	if info.IsDir() {
		return StatusReport{File: f, Status: StatusMissing}
	}
	hash, err := HashFile(f.Path)
	if err != nil {
		return StatusReport{File: f, Status: StatusMissing}
	}
	report := StatusReport{File: f, Hash: hash}
	if f.LastHash == "" {
		report.Status = StatusNew
		return report
	}
	if hash == f.LastHash {
		report.Status = StatusClean
	} else {
		report.Status = StatusModified
	}
	return report
}

type scanner interface {
	Scan(dest ...any) error
}

func scanFile(s scanner) (File, error) {
	var (
		id          int64
		path        string
		display     string
		addedAt     string
		lastHash    string
		lastSynced  string
	)
	if err := s.Scan(&id, &path, &display, &addedAt, &lastHash, &lastSynced); err != nil {
		return File{}, fmt.Errorf("scan tracked_files: %w", err)
	}
	f := File{ID: id, Path: path, DisplayPath: display, LastHash: lastHash}
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

func findByPath(ctx context.Context, s *store.Store, canonical string) (*File, error) {
	row := s.DB().QueryRowContext(ctx,
		`SELECT id, path, display_path, added_at, COALESCE(last_hash,''), COALESCE(last_synced,'')
		 FROM tracked_files WHERE path = ?`, canonical)
	f, err := scanFile(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return &f, nil
}

// findByAnyForm tries multiple normalizations of the user's target string:
// canonical resolution (which requires the file to exist), the literal
// input (could be a display path stored in the DB), and a home-expanded
// form. It returns nil, nil when nothing matches.
func findByAnyForm(ctx context.Context, s *store.Store, target string) (*File, error) {
	candidates := []string{target}

	if expanded, err := expandHome(target); err == nil && expanded != target {
		candidates = append(candidates, expanded)
	}

	if canonical, _, err := Resolve(target); err == nil {
		candidates = append(candidates, canonical)
	}

	for _, c := range candidates {
		if f, err := findByPath(ctx, s, c); err != nil {
			return nil, err
		} else if f != nil {
			return f, nil
		}
	}

	row := s.DB().QueryRowContext(ctx,
		`SELECT id, path, display_path, added_at, COALESCE(last_hash,''), COALESCE(last_synced,'')
		 FROM tracked_files WHERE display_path = ?`, target)
	f, err := scanFile(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return &f, nil
}
