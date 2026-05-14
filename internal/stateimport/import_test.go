package stateimport

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// openTempDB opens a fresh migrated state DB under t.TempDir().
// Mirrors what cmd/dfm tests do but lives in this package so the
// stateimport package can stand on its own.
func openTempDB(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + filepath.Join(dir, "state.db")}}
	s, err := store.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insertTrackedFile(t *testing.T, db *sql.DB, id int64, path string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO tracked_files (id, path, display_path, added_at)
		 VALUES (?, ?, ?, '2024-01-01T00:00:00Z')`, id, path, path)
	if err != nil {
		t.Fatalf("insert tracked_file: %v", err)
	}
}

func insertSnapshot(t *testing.T, db *sql.DB, id, hash string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
		 VALUES (?, NULL, '/x', ?, 1, 'manual', '2024-01-01T00:00:00Z', ?)`,
		id, hash, "/blobs/"+hash)
	if err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestValidateTables_Defaults(t *testing.T) {
	got, err := ValidateTables(nil)
	if err != nil {
		t.Fatalf("ValidateTables: %v", err)
	}
	if len(got) != 2 || got[0] != "tracked_files" || got[1] != "snapshots" {
		t.Fatalf("defaults = %v, want [tracked_files snapshots]", got)
	}
}

func TestValidateTables_RejectsUnknown(t *testing.T) {
	if _, err := ValidateTables([]string{"tracked_files", "passwords"}); err == nil {
		t.Fatal("expected error for unknown table")
	}
}

func TestValidateTables_DependencyOrder(t *testing.T) {
	got, err := ValidateTables([]string{"snapshots", "actions", "tracked_files"})
	if err != nil {
		t.Fatalf("ValidateTables: %v", err)
	}
	want := []string{"tracked_files", "snapshots", "actions"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestImport_BasicCopy(t *testing.T) {
	src := openTempDB(t)
	dst := openTempDB(t)

	insertTrackedFile(t, src.DB(), 1, "/a")
	insertTrackedFile(t, src.DB(), 2, "/b")
	insertSnapshot(t, src.DB(), "snap-1", "aa")

	res, err := Import(context.Background(), Options{
		Source: src.DB(),
		Target: dst.DB(),
		Tables: []string{"tracked_files", "snapshots"},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got, _, _ := res.Totals(); got != 3 {
		t.Fatalf("imported = %d, want 3", got)
	}
	if n := countRows(t, dst.DB(), "tracked_files"); n != 2 {
		t.Fatalf("tracked_files in dst = %d, want 2", n)
	}
	if n := countRows(t, dst.DB(), "snapshots"); n != 1 {
		t.Fatalf("snapshots in dst = %d, want 1", n)
	}
}

func TestImport_DryRunNoWrites(t *testing.T) {
	src := openTempDB(t)
	dst := openTempDB(t)
	insertTrackedFile(t, src.DB(), 1, "/a")

	res, err := Import(context.Background(), Options{
		Source: src.DB(), Target: dst.DB(),
		Tables: []string{"tracked_files"}, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imp, _, _ := res.Totals(); imp != 1 {
		t.Fatalf("dry-run imported count = %d, want 1", imp)
	}
	if n := countRows(t, dst.DB(), "tracked_files"); n != 0 {
		t.Fatalf("dst should be untouched, got %d rows", n)
	}
}

func TestImport_SkipsExistingByDefault(t *testing.T) {
	src := openTempDB(t)
	dst := openTempDB(t)
	insertTrackedFile(t, src.DB(), 1, "/from-src")
	insertTrackedFile(t, dst.DB(), 1, "/from-dst")

	res, err := Import(context.Background(), Options{
		Source: src.DB(), Target: dst.DB(),
		Tables: []string{"tracked_files"},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, skipped, _ := res.Totals(); skipped != 1 {
		t.Fatalf("skipped_existing = %d, want 1", skipped)
	}
	// Verify dst row unchanged.
	var path string
	if err := dst.DB().QueryRow(`SELECT path FROM tracked_files WHERE id=1`).Scan(&path); err != nil {
		t.Fatalf("query: %v", err)
	}
	if path != "/from-dst" {
		t.Fatalf("path = %q, want /from-dst", path)
	}
}

func TestImport_ReplaceOverwrites(t *testing.T) {
	src := openTempDB(t)
	dst := openTempDB(t)
	insertTrackedFile(t, src.DB(), 1, "/from-src")
	insertTrackedFile(t, dst.DB(), 1, "/from-dst")

	res, err := Import(context.Background(), Options{
		Source: src.DB(), Target: dst.DB(),
		Tables: []string{"tracked_files"}, Replace: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imp, _, _ := res.Totals(); imp != 1 {
		t.Fatalf("imported = %d, want 1", imp)
	}
	var path string
	if err := dst.DB().QueryRow(`SELECT path FROM tracked_files WHERE id=1`).Scan(&path); err != nil {
		t.Fatalf("query: %v", err)
	}
	if path != "/from-src" {
		t.Fatalf("path = %q, want /from-src after --replace", path)
	}
}

func TestImport_MissingBlobSkipsSnapshot(t *testing.T) {
	src := openTempDB(t)
	dst := openTempDB(t)
	insertSnapshot(t, src.DB(), "snap-1", "deadbeef")
	insertSnapshot(t, src.DB(), "snap-2", "cafef00d")

	allow := map[string]bool{"deadbeef": true} // only one blob exists locally
	res, err := Import(context.Background(), Options{
		Source: src.DB(), Target: dst.DB(),
		Tables:     []string{"snapshots"},
		BlobExists: func(h string) bool { return allow[h] },
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	imp, _, missing := res.Totals()
	if imp != 1 || missing != 1 {
		t.Fatalf("imported=%d missing=%d, want 1/1", imp, missing)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", res.Warnings)
	}
	if n := countRows(t, dst.DB(), "snapshots"); n != 1 {
		t.Fatalf("dst snapshots = %d, want 1", n)
	}
}

func TestParseTablesFlag(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"tracked_files", []string{"tracked_files"}},
		{" tracked_files , snapshots ", []string{"tracked_files", "snapshots"}},
	}
	for _, tc := range cases {
		got := ParseTablesFlag(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("ParseTablesFlag(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("ParseTablesFlag(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
