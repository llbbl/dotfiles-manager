// Package fsx provides small filesystem helpers shared across packages.
//
// The canonical primitive is AtomicWrite: write-tmp-then-rename with an
// fsync before rename, so a crash between write and rename never leaves
// callers staring at a half-written file.
package fsx

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWrite writes data to path by creating a temp file in the same
// directory, fsyncing it, and renaming it into place. The rename is
// atomic on POSIX filesystems, so concurrent readers see either the
// previous contents or the new contents — never a partial write.
//
// If mode == 0, the existing file's permission bits are preserved. If
// the path does not yet exist (or cannot be stat'd), the mode defaults
// to 0644. Any non-zero mode is applied verbatim to the temp file
// before rename.
//
// Errors are wrapped as: "atomic-write <path>: <step>: <err>".
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	effective := mode
	if effective == 0 {
		effective = 0o644
		if info, err := os.Stat(path); err == nil {
			effective = info.Mode().Perm()
		}
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomic-write %s: create temp: %w", path, err)
	}
	tmpName := tmp.Name()

	// armed=true means we still own the temp file and must remove it on
	// any failure. Set to false once Rename succeeds.
	armed := true
	defer func() {
		if armed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomic-write %s: write temp: %w", path, err)
	}
	if err := tmp.Chmod(effective); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomic-write %s: chmod temp: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomic-write %s: sync temp: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomic-write %s: close temp: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomic-write %s: rename: %w", path, err)
	}
	armed = false
	return nil
}
