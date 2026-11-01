package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	turso_libs "github.com/tursodatabase/turso-go-platform-libs"
)

var (
	nativeLibOnce sync.Once
	nativeLibErr  error

	// ponytail: fixed 30s wait on a hung holder; make it configurable if anyone hits it.
	lockTimeout = 30 * time.Second
)

// ensureNativeLib extracts tursogo's embedded library under a cross-process
// lock. Upstream writes it in place with no lock, so concurrent cold starts
// read a half-written file and tursogo panics.
func ensureNativeLib() error {
	nativeLibOnce.Do(func() { nativeLibErr = loadNativeLib() })
	return nativeLibErr
}

func loadNativeLib() error {
	dir := tursoCacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create turso cache dir: %w", err)
	}
	unlock, err := lockFile(filepath.Join(dir, ".dfm.lock"))
	if err != nil {
		return err
	}
	defer unlock()

	_, err = turso_libs.LoadTursoLibrary(turso_libs.LoadTursoLibraryConfig{})
	if err != nil && strings.Contains(err.Error(), "hash sum mismatch") {
		// The hash prefix naming the subdirectory isn't exported, so clear them all.
		if err := clearCacheDirs(dir); err != nil {
			return err
		}
		_, err = turso_libs.LoadTursoLibrary(turso_libs.LoadTursoLibraryConfig{})
	}
	if err != nil {
		return fmt.Errorf("load turso native library: %w", err)
	}
	return nil
}

// ponytail: must track upstream embeddedLibraryTryCreate in turso-go-platform-libs;
// if its cache root changes, the lock no longer guards the file it extracts.
func tursoCacheDir() string {
	root := os.Getenv("TURSO_GO_CACHE_DIR")
	if root == "" {
		if d, err := os.UserCacheDir(); err == nil {
			root = d
		} else {
			root = os.TempDir()
		}
	}
	return filepath.Join(root, "turso-go")
}

func clearCacheDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read turso cache dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("clear turso cache: %w", err)
		}
	}
	return nil
}
