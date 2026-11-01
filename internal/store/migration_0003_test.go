package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// fileLinks maps each snapshot and suggestion id to the path of the file it
// points at ("" for NULL), which is the identity that must survive a rebuild.
func fileLinks(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"snapshots", "suggestions"} {
		rows, err := db.Query(`SELECT c.id, COALESCE(f.path, ''), c.file_id IS NULL
			FROM ` + table + ` c LEFT JOIN tracked_files f ON f.id = c.file_id`)
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		for rows.Next() {
			var id, path string
			var isNull bool
			if err := rows.Scan(&id, &path, &isNull); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			if !isNull && path == "" {
				t.Errorf("%s %s has a dangling file_id", table, id)
			}
			out[table+":"+id] = path
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestMigration0003_UpAndDown_PreservesFileLinks(t *testing.T) {
	ctx, db, cleanup := openTestDB(t)
	defer cleanup()

	if err := RunGoose(ctx, db, "up-to", "2"); err != nil {
		t.Fatalf("up-to 2: %v", err)
	}
	mustExec(t, db, `INSERT INTO tracked_files (id, path, display_path, added_at) VALUES
		(1, '/a', '~/a', '2024-01-01T00:00:00Z'),
		(2, '/b', '~/b', '2024-01-01T00:00:00Z'),
		(5, '/c', '~/c', '2024-01-01T00:00:00Z')`)
	// Dangling ids predate FK enforcement, so seed them with it off on a pinned
	// conn, then switch it back so the migration itself runs enforced.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path) VALUES
		('s1', 1, '/a', 'h1', 1, 'manual', '2024-01-01T00:00:00Z', '/blob1'),
		('s2', 5, '/c', 'h2', 1, 'manual', '2024-01-01T00:00:00Z', '/blob2'),
		('s3', NULL, '/x', 'h3', 1, 'manual', '2024-01-01T00:00:00Z', '/blob3'),
		('s4', 99, '/gone', 'h4', 1, 'manual', '2024-01-01T00:00:00Z', '/blob4')`,
		`INSERT INTO suggestions (id, file_id, provider, prompt, diff, status, created_at) VALUES
		('g1', 2, 'p', 'pr', 'd', 'pending', '2024-01-01T00:00:00Z'),
		('g2', 1, 'p', 'pr', 'd', 'pending', '2024-01-01T00:00:00Z'),
		('g3', 42, 'p', 'pr', 'd', 'pending', '2024-01-01T00:00:00Z')`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	_ = conn.Close()

	want := map[string]string{
		"snapshots:s1": "/a", "snapshots:s2": "/c", "snapshots:s3": "", "snapshots:s4": "",
		"suggestions:g1": "/b", "suggestions:g2": "/a", "suggestions:g3": "",
	}

	if err := RunGoose(ctx, db, "up-to", "3"); err != nil {
		t.Fatalf("up-to 3: %v", err)
	}
	assertLinks(t, db, want)
	assertNullFileIDs(t, db, []string{"s3", "s4"}, []string{"g3"})

	rows, err := db.Query(`SELECT id, typeof(id) FROM tracked_files`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var id, typ string
		if err := rows.Scan(&id, &typ); err != nil {
			t.Fatal(err)
		}
		if typ != "text" || id == "" || seen[id] {
			t.Errorf("tracked_files id %q (%s): want unique non-empty text", id, typ)
		}
		seen[id] = true
	}
	_ = rows.Close()
	if len(seen) != 3 {
		t.Errorf("tracked_files rows = %d, want 3", len(seen))
	}
	assertSchema(t, db)

	if err := RunGoose(ctx, db, "down-to", "2"); err != nil {
		t.Fatalf("down-to 2: %v", err)
	}
	assertLinks(t, db, want)
	var bad int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tracked_files WHERE typeof(id) != 'integer'`).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Errorf("%d tracked_files ids are not integers after down", bad)
	}
	assertSchema(t, db)
}

func assertLinks(t *testing.T, db *sql.DB, want map[string]string) {
	t.Helper()
	got := fileLinks(t, db)
	if len(got) != len(want) {
		t.Errorf("links = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s -> %q, want %q", k, got[k], v)
		}
	}
}

func assertNullFileIDs(t *testing.T, db *sql.DB, snaps, sugs []string) {
	t.Helper()
	for table, ids := range map[string][]string{"snapshots": snaps, "suggestions": sugs} {
		for _, id := range ids {
			var isNull bool
			if err := db.QueryRow(`SELECT file_id IS NULL FROM `+table+` WHERE id = ?`, id).Scan(&isNull); err != nil {
				t.Fatalf("%s %s: %v", table, id, err)
			}
			if !isNull {
				t.Errorf("%s %s file_id should be NULL", table, id)
			}
		}
	}
}

func assertSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	names := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type IN ('table', 'index')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names[n] = true
	}
	_ = rows.Close()
	for _, n := range []string{"tracked_files", "snapshots", "suggestions",
		"idx_snapshots_path", "idx_snapshots_hash", "idx_snapshots_file_id", "idx_snapshots_created_at"} {
		if !names[n] {
			t.Errorf("missing %s", n)
		}
	}
	for _, n := range []string{"file_id_map", "tracked_files_old", "snapshots_old", "suggestions_old"} {
		if names[n] {
			t.Errorf("leftover %s", n)
		}
	}
}

func TestOpen_EnforcesForeignKeysOnEveryPooledConnection(t *testing.T) {
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + filepath.Join(t.TempDir(), "state.db")}}
	ctx := context.Background()
	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Holding every conn open forces the pool to dial a new one each time.
	const n = 4
	conns := make([]*sql.Conn, 0, n)
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	for i := range n {
		c, err := s.DB().Conn(ctx)
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	if got := s.DB().Stats().OpenConnections; got < n {
		t.Fatalf("open connections = %d, want >= %d", got, n)
	}
	for i, c := range conns {
		_, err := c.ExecContext(ctx,
			`INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
			 VALUES (?, 'no-such-file', '/x', 'h', 1, 'manual', '2024-01-01T00:00:00Z', '/b')`,
			"snap-"+string(rune('a'+i)))
		if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Errorf("conn %d: dangling file_id insert err = %v, want FOREIGN KEY failure", i, err)
		}
	}
}
