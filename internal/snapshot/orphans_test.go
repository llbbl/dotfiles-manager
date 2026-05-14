package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func writeBlob(t *testing.T, root, sha string, content string) string {
	t.Helper()
	dir := filepath.Join(root, sha[:2])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, sha)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestFindOrphans_MissingRoot_ReturnsEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	paths, bytes, err := FindOrphans(root, map[string]struct{}{})
	if err != nil {
		t.Fatalf("FindOrphans: %v", err)
	}
	if len(paths) != 0 || bytes != 0 {
		t.Fatalf("expected empty result, got %d paths, %d bytes", len(paths), bytes)
	}
}

func TestFindOrphans_EmptyRoot_ReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	paths, bytes, err := FindOrphans(root, map[string]struct{}{})
	if err != nil {
		t.Fatalf("FindOrphans: %v", err)
	}
	if len(paths) != 0 || bytes != 0 {
		t.Fatalf("expected empty result, got %d paths, %d bytes", len(paths), bytes)
	}
}

func TestFindOrphans_AllReferenced_ReturnsNone(t *testing.T) {
	root := t.TempDir()
	writeBlob(t, root, shaA, "alpha")
	writeBlob(t, root, shaB, "bravo")

	refs := map[string]struct{}{shaA: {}, shaB: {}}
	paths, bytes, err := FindOrphans(root, refs)
	if err != nil {
		t.Fatalf("FindOrphans: %v", err)
	}
	if len(paths) != 0 || bytes != 0 {
		t.Fatalf("expected no orphans, got %d (%d bytes)", len(paths), bytes)
	}
}

func TestFindOrphans_SomeOrphans_ReturnsThem(t *testing.T) {
	root := t.TempDir()
	writeBlob(t, root, shaA, "keep")
	bPath := writeBlob(t, root, shaB, "drop-me")
	cPath := writeBlob(t, root, shaC, "drop-too")

	refs := map[string]struct{}{shaA: {}}
	paths, bytes, err := FindOrphans(root, refs)
	if err != nil {
		t.Fatalf("FindOrphans: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 orphans, got %d: %v", len(paths), paths)
	}
	got := map[string]bool{paths[0]: true, paths[1]: true}
	if !got[bPath] || !got[cPath] {
		t.Fatalf("orphan set mismatch: %v", paths)
	}
	want := int64(len("drop-me") + len("drop-too"))
	if bytes != want {
		t.Fatalf("bytes = %d, want %d", bytes, want)
	}
}

func TestFindOrphans_IgnoresNonHexAndTmpFiles(t *testing.T) {
	root := t.TempDir()
	writeBlob(t, root, shaA, "kept")
	// Stray files we should NOT report as orphans.
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Join(root, shaB[:2])
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, shaB+".tmp"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "not-a-sha"), []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths, _, err := FindOrphans(root, map[string]struct{}{shaA: {}})
	if err != nil {
		t.Fatalf("FindOrphans: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("expected 0 orphans, got %v", paths)
	}
}

func TestRemoveOrphans_DeletesFilesAndEmptyShards(t *testing.T) {
	root := t.TempDir()
	keep := writeBlob(t, root, shaA, "keep")
	drop := writeBlob(t, root, shaB, "drop")

	if err := RemoveOrphans(root, []string{drop}); err != nil {
		t.Fatalf("RemoveOrphans: %v", err)
	}
	if _, err := os.Stat(drop); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed; err=%v", drop, err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("expected %s to remain; err=%v", keep, err)
	}
	// shaB's shard dir should be gone (it became empty); shaA's must remain.
	if _, err := os.Stat(filepath.Join(root, shaB[:2])); !os.IsNotExist(err) {
		t.Fatalf("expected shard %s pruned; err=%v", shaB[:2], err)
	}
	if _, err := os.Stat(filepath.Join(root, shaA[:2])); err != nil {
		t.Fatalf("expected shard %s to remain; err=%v", shaA[:2], err)
	}
}

func TestRemoveOrphans_IgnoresMissingFiles(t *testing.T) {
	root := t.TempDir()
	bogus := filepath.Join(root, shaA[:2], shaA)
	if err := RemoveOrphans(root, []string{bogus}); err != nil {
		t.Fatalf("RemoveOrphans on missing path should not error: %v", err)
	}
}

func TestIsHexSha256(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{strings.Repeat("a", 64), true},
		{strings.Repeat("A", 64), false}, // uppercase not allowed
		{strings.Repeat("g", 64), false}, // non-hex
		{strings.Repeat("a", 63), false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isHexSha256(tc.in); got != tc.want {
			t.Errorf("isHexSha256(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
