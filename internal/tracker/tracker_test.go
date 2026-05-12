package tracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + filepath.Join(dir, "state.db")}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// withCwd swaps the working directory for the duration of t.
func withCwd(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolve_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	p := filepath.Join(dir, "file.txt")
	writeFile(t, p, "hello\n")

	canonical, display, err := Resolve(p)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(canonical, "file.txt") {
		t.Errorf("canonical = %q", canonical)
	}
	if display == "" {
		t.Errorf("empty display")
	}
}

func TestResolve_RelativePath(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	writeFile(t, filepath.Join(dir, "rel.txt"), "x")

	canonical, _, err := Resolve("./rel.txt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(canonical, "rel.txt") {
		t.Errorf("canonical = %q", canonical)
	}
}

func TestResolve_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	rhome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Skip("home symlink eval failed")
	}
	f, err := os.CreateTemp(rhome, "dotfiles-tracker-test-*")
	if err != nil {
		t.Skipf("can't create temp under home: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	f.Close()

	base := filepath.Base(f.Name())
	canonical, display, err := Resolve("~/" + base)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(canonical, base) {
		t.Errorf("canonical = %q", canonical)
	}
	if !strings.HasPrefix(display, "~/") {
		t.Errorf("display = %q, want ~/-prefixed", display)
	}
}

func TestResolve_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	if _, _, err := Resolve(dir); !errors.Is(err, ErrIsDirectory) {
		t.Errorf("want ErrIsDirectory, got %v", err)
	}
}

func TestResolve_RejectsSystemPath(t *testing.T) {
	if _, err := os.Stat("/etc/hosts"); err != nil {
		t.Skip("/etc/hosts not present")
	}
	if _, _, err := Resolve("/etc/hosts"); !errors.Is(err, ErrPathOutsideAllowed) {
		t.Errorf("want ErrPathOutsideAllowed, got %v", err)
	}
}

func TestResolve_RejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	t.Setenv("HOME", dir)

	// Target a system path (/usr/bin or /etc); the resolved path will
	// be rejected by the system-root guard regardless of HOME/tmp.
	var target string
	for _, candidate := range []string{"/usr/bin/true", "/etc/hosts"} {
		if _, err := os.Stat(candidate); err == nil {
			target = candidate
			break
		}
	}
	if target == "" {
		t.Skip("no system file available for escape target")
	}

	link := filepath.Join(dir, "escape.lnk")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if _, _, err := Resolve(link); !errors.Is(err, ErrPathOutsideAllowed) {
		t.Errorf("want ErrPathOutsideAllowed, got %v", err)
	}
}

func TestTrackListUntrack_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	t.Setenv("HOME", dir)
	s := newTestStore(t)
	ctx := context.Background()

	names := []string{"a.txt", "b.txt", "c.txt"}
	for _, n := range names {
		p := filepath.Join(dir, n)
		writeFile(t, p, "content of "+n)
		canon, disp, err := Resolve(p)
		if err != nil {
			t.Fatalf("Resolve %s: %v", n, err)
		}
		if _, err := Track(ctx, s, canon, disp, TrackOptions{}); err != nil {
			t.Fatalf("Track %s: %v", n, err)
		}
	}

	files, err := List(ctx, s)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d", len(files))
	}
	for i := 1; i < len(files); i++ {
		if files[i-1].DisplayPath > files[i].DisplayPath {
			t.Errorf("not sorted: %v", files)
		}
	}

	if _, err := Untrack(ctx, s, files[0].Path); err != nil {
		t.Fatalf("Untrack: %v", err)
	}
	files2, _ := List(ctx, s)
	if len(files2) != 2 {
		t.Errorf("after untrack want 2, got %d", len(files2))
	}
}

func TestTrack_DuplicateRequiresReset(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	t.Setenv("HOME", dir)
	s := newTestStore(t)
	ctx := context.Background()

	p := filepath.Join(dir, "dup.txt")
	writeFile(t, p, "v1")
	canon, disp, err := Resolve(p)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := Track(ctx, s, canon, disp, TrackOptions{}); err != nil {
		t.Fatalf("first Track: %v", err)
	}
	_, err = Track(ctx, s, canon, disp, TrackOptions{})
	if !errors.Is(err, ErrAlreadyTracked) {
		t.Errorf("want ErrAlreadyTracked, got %v", err)
	}

	writeFile(t, p, "v2-different")
	f, err := Track(ctx, s, canon, disp, TrackOptions{Reset: true})
	if err != nil {
		t.Fatalf("Reset Track: %v", err)
	}
	if f.LastHash == "" {
		t.Errorf("LastHash empty after reset")
	}
}

func TestUntrack_NotTracked(t *testing.T) {
	s := newTestStore(t)
	if _, err := Untrack(context.Background(), s, "/nope/never"); !errors.Is(err, ErrNotTracked) {
		t.Errorf("want ErrNotTracked, got %v", err)
	}
}

func TestComputeStatus_CleanModifiedMissing(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	t.Setenv("HOME", dir)
	s := newTestStore(t)
	ctx := context.Background()

	clean := filepath.Join(dir, "clean.txt")
	modified := filepath.Join(dir, "modified.txt")
	missing := filepath.Join(dir, "missing.txt")
	writeFile(t, clean, "stay")
	writeFile(t, modified, "before")
	writeFile(t, missing, "willvanish")

	for _, p := range []string{clean, modified, missing} {
		c, d, err := Resolve(p)
		if err != nil {
			t.Fatalf("Resolve %s: %v", p, err)
		}
		if _, err := Track(ctx, s, c, d, TrackOptions{}); err != nil {
			t.Fatalf("Track: %v", err)
		}
	}

	if err := os.WriteFile(modified, []byte("after-different"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}

	reports, err := ComputeStatus(ctx, s)
	if err != nil {
		t.Fatalf("ComputeStatus: %v", err)
	}

	got := map[string]Status{}
	for _, r := range reports {
		got[filepath.Base(r.File.Path)] = r.Status
	}
	if got["clean.txt"] != StatusClean {
		t.Errorf("clean.txt = %s", got["clean.txt"])
	}
	if got["modified.txt"] != StatusModified {
		t.Errorf("modified.txt = %s", got["modified.txt"])
	}
	if got["missing.txt"] != StatusMissing {
		t.Errorf("missing.txt = %s", got["missing.txt"])
	}
}

func TestTrack_SecretPreflight(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	t.Setenv("HOME", dir)
	s := newTestStore(t)
	ctx := context.Background()

	p := filepath.Join(dir, "creds.txt")
	writeFile(t, p, "AKIATEST0000000000AB\n")
	canon, disp, err := Resolve(p)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	_, err = Track(ctx, s, canon, disp, TrackOptions{})
	var secErr *SecretsError
	if !errors.As(err, &secErr) {
		t.Fatalf("want SecretsError, got %v", err)
	}
	if len(secErr.Result.Findings) == 0 {
		t.Errorf("expected findings")
	}

	if _, err := Track(ctx, s, canon, disp, TrackOptions{SkipSecretCheck: true}); err != nil {
		t.Errorf("force Track: %v", err)
	}
}

func TestTrack_TakesInitialSnapshot(t *testing.T) {
	dir := t.TempDir()
	withCwd(t, dir)
	t.Setenv("HOME", dir)
	s := newTestStore(t)
	ctx := context.Background()

	p := filepath.Join(dir, "snap.txt")
	writeFile(t, p, "snapshot-me\n")
	canon, disp, err := Resolve(p)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var calledWith File
	called := 0
	_, err = Track(ctx, s, canon, disp, TrackOptions{
		AfterCommit: func(_ context.Context, f File) error {
			called++
			calledWith = f
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if called != 1 {
		t.Errorf("AfterCommit called %d times, want 1", called)
	}
	if calledWith.Path != canon {
		t.Errorf("AfterCommit path = %q", calledWith.Path)
	}
	if calledWith.ID == 0 {
		t.Errorf("AfterCommit File.ID = 0")
	}
}

func TestHashFile_Stable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	writeFile(t, p, "abc")
	h, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("abc") = ba7816bf...
	if !strings.HasPrefix(h, "ba7816bf") {
		t.Errorf("hash = %q", h)
	}
}
