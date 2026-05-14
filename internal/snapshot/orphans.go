package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FindOrphans walks blobRoot and returns blobs whose SHA does not
// appear in the referenced set. A blob's SHA is taken from its
// filename (the sha-prefix shard directory is informational only).
// Returns the orphan paths and their cumulative on-disk byte size.
//
// If blobRoot does not exist, FindOrphans returns no error: a
// missing root is treated as "no blobs".
//
// Files whose names are not 64 hex characters (the expected sha256
// hex length) are ignored: they are not blobs the snapshot manager
// owns, so we don't presume to remove them. Same for the *.tmp
// rename-staging files snapshot.Manager produces during writes.
func FindOrphans(blobRoot string, referenced map[string]struct{}) ([]string, int64, error) {
	if blobRoot == "" {
		return nil, 0, errors.New("blobRoot is empty")
	}
	info, err := os.Stat(blobRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("stat blob root: %w", err)
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("blob root is not a directory: %s", blobRoot)
	}

	var (
		orphans []string
		bytes   int64
	)
	walkErr := filepath.WalkDir(blobRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".tmp") {
			return nil
		}
		if !isHexSha256(name) {
			return nil
		}
		if _, ok := referenced[name]; ok {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		orphans = append(orphans, path)
		bytes += fi.Size()
		return nil
	})
	if walkErr != nil {
		return nil, 0, walkErr
	}
	return orphans, bytes, nil
}

// RemoveOrphans removes the given blob paths and then prunes any
// sha-prefix shard directories under blobRoot that have become
// empty. Returns first error encountered (callers may want to keep
// going on ErrNotExist; we surface only real failures).
func RemoveOrphans(blobRoot string, paths []string) error {
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return pruneEmptyShards(blobRoot)
}

// pruneEmptyShards removes empty immediate-child directories of
// blobRoot (the two-character sha-prefix shards). Not recursive:
// the snapshot manager only creates one level of sharding.
func pruneEmptyShards(blobRoot string) error {
	entries, err := os.ReadDir(blobRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read blob root: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(blobRoot, e.Name())
		sub, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read shard %s: %w", dir, err)
		}
		if len(sub) == 0 {
			if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove shard %s: %w", dir, err)
			}
		}
	}
	return nil
}

// ReferencedHashes returns the set of content hashes currently
// referenced by any snapshot row in the state DB. This is the set
// FindOrphans compares against.
//
// Audit / actions / tracked_files tables are intentionally NOT
// queried: they don't own blob content. The suggestions table
// stores diff text inline (TEXT column), not blob refs.
func (m *Manager) ReferencedHashes(ctx context.Context) (map[string]struct{}, error) {
	rows, err := m.s.DB().QueryContext(ctx, `SELECT DISTINCT hash FROM snapshots`)
	if err != nil {
		return nil, fmt.Errorf("query snapshot hashes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	refs := map[string]struct{}{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		refs[h] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return refs, nil
}

func isHexSha256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
