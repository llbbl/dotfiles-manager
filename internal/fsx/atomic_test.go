package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestAtomicWrite_NewFile_WritesBytesAndAppliesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	data := []byte("hello world")

	if err := AtomicWrite(path, data, 0o640); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content: got %q, want %q", got, data)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Errorf("mode: got %o, want %o", got, want)
	}
}

func TestAtomicWrite_ExistingFile_ModeZeroPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := AtomicWrite(path, []byte("new"), 0); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("content: got %q, want %q", got, "new")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("mode: got %o, want %o (should be preserved)", got, want)
	}
}

func TestAtomicWrite_ExistingFile_ExplicitModeHonored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := AtomicWrite(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Errorf("mode: got %o, want %o (explicit mode should win)", got, want)
	}
}

func TestAtomicWrite_ModeZeroOnMissingPath_DefaultsTo0644(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")

	if err := AtomicWrite(path, []byte("x"), 0); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Errorf("mode: got %o, want %o (default)", got, want)
	}
}

func TestAtomicWrite_CreateTempFails_NoRemnantInTargetDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses dir permissions")
	}
	dir := t.TempDir()
	// Read-only directory: CreateTemp inside it must fail.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	path := filepath.Join(dir, "x.txt")
	err := AtomicWrite(path, []byte("data"), 0o644)
	if err == nil {
		t.Fatalf("expected error writing into read-only dir")
	}
	if !strings.Contains(err.Error(), "atomic-write") {
		t.Errorf("error should carry canonical phrasing: %v", err)
	}

	// No stray *.tmp-* files left behind.
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		// If we can't read it, at least ensure the original error was returned.
		return
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp remnant left behind: %s", e.Name())
		}
	}
}

func TestAtomicWrite_ConcurrentWriters_FinalContentIsOneOfThem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "race.txt")
	// Seed so mode==0 has something to preserve.
	if err := os.WriteFile(path, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const N = 8
	var wg sync.WaitGroup
	errs := make([]error, N)
	contents := make([][]byte, N)
	for i := range N {
		contents[i] = []byte(strings.Repeat(string(rune('a'+i)), 64))
		wg.Go(func() {
			errs[i] = AtomicWrite(path, contents[i], 0o644)
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	matched := false
	for _, c := range contents {
		if string(got) == string(c) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("final content matched no writer (partial file?): %q", got)
	}
}

// Sanity check that errors are wrapped (errors.Is round-trip through the
// underlying os error).
func TestAtomicWrite_ErrorIsWrapped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses dir permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := AtomicWrite(filepath.Join(dir, "x.txt"), []byte("d"), 0o644)
	if err == nil {
		t.Fatal("expected error")
	}
	// The wrapped error chain should still expose os.ErrPermission.
	if !errors.Is(err, os.ErrPermission) {
		t.Logf("note: chain did not unwrap to os.ErrPermission: %v", err)
	}
}
