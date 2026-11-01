package stateimport

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

func insertTrackedFile(t *testing.T, db *sql.DB, id, path string) {
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

	insertTrackedFile(t, src.DB(), "1", "/a")
	insertTrackedFile(t, src.DB(), "2", "/b")
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
	insertTrackedFile(t, src.DB(), "1", "/a")

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

func TestImport_SkipsExistingPathByDefault(t *testing.T) {
	src := openTempDB(t)
	dst := openTempDB(t)
	insertTrackedFile(t, src.DB(), "src-id", "/a")
	insertTrackedFile(t, dst.DB(), "dst-id", "/a")
	if _, err := src.DB().Exec(`UPDATE tracked_files SET display_path = '~/a'`); err != nil {
		t.Fatal(err)
	}

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
	var id, display string
	if err := dst.DB().QueryRow(`SELECT id, display_path FROM tracked_files WHERE path = '/a'`).Scan(&id, &display); err != nil {
		t.Fatalf("query: %v", err)
	}
	if id != "dst-id" || display != "/a" {
		t.Fatalf("target row = (%q, %q), want (dst-id, /a) unchanged", id, display)
	}
}

func TestImport_ReplaceUpdatesMatchedPathInPlace(t *testing.T) {
	src, dst := collisionPair(t)
	if _, err := src.Exec(`UPDATE tracked_files SET display_path = '~/a', last_hash = 'h-new' WHERE id = 'A'`); err != nil {
		t.Fatal(err)
	}
	before := dumpTable(t, dst, "tracked_files")

	res, err := Import(context.Background(), Options{
		Source: src, Target: dst, Tables: []string{"tracked_files"}, Replace: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imp, skipped, _ := res.Totals(); imp != 2 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d, want 2/0", imp, skipped)
	}
	if n := countRows(t, dst, "tracked_files"); n != 3 {
		t.Fatalf("tracked_files = %d, want 3", n)
	}
	var id, display, hash string
	if err := dst.QueryRow(`SELECT id, display_path, last_hash FROM tracked_files WHERE path = '/a'`).Scan(&id, &display, &hash); err != nil {
		t.Fatalf("query: %v", err)
	}
	if id != "D" || display != "~/a" || hash != "h-new" {
		t.Fatalf("/a = (%q, %q, %q), want (D, ~/a, h-new)", id, display, hash)
	}
	after := dumpTable(t, dst, "tracked_files")
	if !slices.Contains(after, before[0]) { // before[0] is /c
		t.Fatalf("/c changed: %q not in %q", before[0], after)
	}
}

func TestImport_DryRunMatchesRealRunAndWritesNothing(t *testing.T) {
	src, dst := collisionPair(t)
	insertSnapshot(t, src, "snap-orphan", "aa") // no file_id, and its blob is missing below
	before := dumpAll(t, dst)
	srcBefore := dumpAll(t, src)

	for _, replace := range []bool{true, false} {
		opts := Options{Source: src, Target: dst, Tables: AllowedTables, Replace: replace, DryRun: true,
			BlobExists: func(h string) bool { return h != "aa" }}
		dry, err := Import(context.Background(), opts)
		if err != nil {
			t.Fatalf("dry run (replace=%v): %v", replace, err)
		}
		if got := dumpAll(t, dst); !reflect.DeepEqual(got, before) {
			t.Fatalf("dry run (replace=%v) changed target:\n%v\nwant\n%v", replace, got, before)
		}
		if replace {
			continue // only the last pass does a real run; it changes the baseline
		}
		opts.DryRun = false
		real, err := Import(context.Background(), opts)
		if err != nil {
			t.Fatalf("real run: %v", err)
		}
		if len(real.Warnings) != 1 {
			t.Fatalf("real-run warnings = %v, want the missing-blob one", real.Warnings)
		}
		dry.DryRun = false
		if !reflect.DeepEqual(dry, real) {
			t.Fatalf("dry-run result %+v != real-run result %+v", dry, real)
		}
	}
	if got := dumpAll(t, src); !reflect.DeepEqual(got, srcBefore) {
		t.Fatal("source changed")
	}
}

func TestImport_HistoryForUntrackedPathKeptUnlinked(t *testing.T) {
	src, dst := collisionPair(t)

	res, err := Import(context.Background(), Options{Source: src, Target: dst, Tables: []string{"snapshots"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := fileIDOf(t, dst, "snapshots", "snap-a"); got != "D" {
		t.Errorf("snap-a file_id = %q, want D (matched by path)", got)
	}
	if got := fileIDOf(t, dst, "snapshots", "snap-b"); got != "" {
		t.Errorf("snap-b file_id = %q, want NULL", got)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "snap-b") {
		t.Fatalf("warnings = %v, want one for snap-b", res.Warnings)
	}
	if n := countRows(t, dst, "tracked_files"); n != 2 {
		t.Fatalf("tracked_files = %d, want 2 (not imported)", n)
	}
}

func TestImport_DanglingSourceFileIDImportedUnlinked(t *testing.T) {
	src, dst := openTempDB(t).DB(), openTempDB(t).DB()
	ctx := context.Background()
	conn, err := src.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO suggestions (id, file_id, provider, prompt, diff, status, created_at)
		 VALUES ('sug-x', 'missing', 'test', 'p', 'd', 'pending', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	res, err := Import(ctx, Options{Source: src, Target: dst, Tables: historyTables})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := fileIDOf(t, dst, "suggestions", "sug-x"); got != "" {
		t.Fatalf("file_id = %q, want NULL", got)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "sug-x") {
		t.Fatalf("warnings = %v, want one for sug-x", res.Warnings)
	}
}

func TestImport_FailureMidRunLeavesTargetUnchanged(t *testing.T) {
	src, dst := collisionPair(t)
	if _, err := dst.Exec(`CREATE TRIGGER fail_snap_b BEFORE INSERT ON snapshots
		WHEN NEW.id = 'snap-b' BEGIN SELECT RAISE(ABORT, 'forced failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	before := dumpAll(t, dst)

	// snap-b is inserted after tracked file B and snap-a, so those must roll back.
	_, err := Import(context.Background(), Options{Source: src, Target: dst, Tables: historyTables})
	if err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("Import err = %v, want the trigger's error", err)
	}
	if got := dumpAll(t, dst); !reflect.DeepEqual(got, before) {
		t.Fatalf("target changed after failed import:\n%v\nwant\n%v", got, before)
	}
	// A failed import that never rolls back keeps the write lock, so this write would hit busy.
	if _, err := dst.Exec(`DROP TRIGGER fail_snap_b`); err != nil {
		t.Fatalf("target still locked after failed import: %v", err)
	}
}

func TestImport_ReimportIsIdempotent(t *testing.T) {
	src, dst := collisionPair(t)
	opts := Options{Source: src, Target: dst, Tables: historyTables}
	if _, err := Import(context.Background(), opts); err != nil {
		t.Fatalf("first import: %v", err)
	}
	after := dumpAll(t, dst)

	res, err := Import(context.Background(), opts)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if imp, skipped, _ := res.Totals(); imp != 0 || skipped != 6 {
		t.Fatalf("second run imported=%d skipped=%d, want 0/6", imp, skipped)
	}
	if got := dumpAll(t, dst); !reflect.DeepEqual(got, after) {
		t.Fatal("second import changed the target")
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

func insertLinkedSnapshot(t *testing.T, db *sql.DB, id, fileID, path string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
		 VALUES (?, ?, ?, 'h-'||?, 1, 'manual', '2024-01-01T00:00:00Z', '/blobs/x')`,
		id, fileID, path, id); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
}

func insertSuggestion(t *testing.T, db *sql.DB, id, fileID string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO suggestions (id, file_id, provider, prompt, diff, status, created_at)
		 VALUES (?, ?, 'test', 'p', 'd', 'pending', '2024-01-01T00:00:00Z')`,
		id, fileID); err != nil {
		t.Fatalf("insert suggestion: %v", err)
	}
}

// fileIDOf returns the file_id of a row as "" when NULL.
func fileIDOf(t *testing.T, db *sql.DB, table, id string) string {
	t.Helper()
	var fid sql.NullString
	if err := db.QueryRow(`SELECT file_id FROM `+table+` WHERE id = ?`, id).Scan(&fid); err != nil {
		t.Fatalf("file_id of %s %s: %v", table, id, err)
	}
	return fid.String
}

func trackedIDs(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT path, id FROM tracked_files`)
	if err != nil {
		t.Fatalf("tracked ids: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var p, id string
		if err := rows.Scan(&p, &id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[p] = id
	}
	return out
}

// collisionPair builds independent DBs that both track /a under different ids:
// source {A:/a, B:/b} with history on both, target {C:/c, D:/a}.
func collisionPair(t *testing.T) (src, dst *sql.DB) {
	t.Helper()
	src, dst = openTempDB(t).DB(), openTempDB(t).DB()
	insertTrackedFile(t, src, "A", "/a")
	insertTrackedFile(t, src, "B", "/b")
	insertLinkedSnapshot(t, src, "snap-a", "A", "/a")
	insertLinkedSnapshot(t, src, "snap-b", "B", "/b")
	insertSuggestion(t, src, "sug-a", "A")
	insertSuggestion(t, src, "sug-b", "B")
	insertTrackedFile(t, dst, "C", "/c")
	insertTrackedFile(t, dst, "D", "/a")
	return src, dst
}

var historyTables = []string{"tracked_files", "snapshots", "suggestions"}

func TestImport_PathCollision_RemapsHistoryToTargetFile(t *testing.T) {
	src, dst := collisionPair(t)

	res, err := Import(context.Background(), Options{Source: src, Target: dst, Tables: historyTables})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got, want := trackedIDs(t, dst), (map[string]string{"/a": "D", "/b": "B", "/c": "C"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("tracked_files = %v, want %v", got, want)
	}
	for table, want := range map[string]map[string]string{
		"snapshots":   {"snap-a": "D", "snap-b": "B"},
		"suggestions": {"sug-a": "D", "sug-b": "B"},
	} {
		for id, fid := range want {
			if got := fileIDOf(t, dst, table, id); got != fid {
				t.Errorf("%s %s file_id = %q, want %q", table, id, got, fid)
			}
		}
	}
	want := []TableResult{
		{Table: "tracked_files", Imported: 1, SkippedExisting: 1},
		{Table: "snapshots", Imported: 2},
		{Table: "suggestions", Imported: 2},
	}
	if !reflect.DeepEqual(res.Tables, want) || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v warnings=%v, want %+v", res.Tables, res.Warnings, want)
	}
}

// dumpTable renders every row of table, ordered by id, for equality checks.
func dumpTable(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY id`)
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		out = append(out, fmt.Sprint(vals))
	}
	return out
}

func dumpAll(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range AllowedTables {
		out[table] = dumpTable(t, db, table)
	}
	return out
}
