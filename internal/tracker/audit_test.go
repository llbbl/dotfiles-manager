package tracker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
)

func setupAuditLogger(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "audit.jsonl")
	cfg := &config.Config{
		Log: config.LogConfig{
			Backend: "jsonl",
			Path:    path,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l, err := audit.New(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	audit.SetDefault(l)
	t.Cleanup(func() { audit.SetDefault(nil) })
	return path
}

func readAuditLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	var out []map[string]any
	for ln := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRecordHashChange_UpdatesRowAndEmitsAudit(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	auditPath := setupAuditLogger(t, dir)

	ctx := context.Background()

	// Insert a row directly so we have a known ID + old hash.
	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (path, display_path, added_at, last_hash) VALUES (?, ?, ?, ?)`,
		"/tmp/x", "~/x", time.Now().UTC().Format(time.RFC3339), "oldhash")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()

	f := File{ID: id, Path: "/tmp/x", DisplayPath: "~/x", LastHash: "oldhash"}
	err = RecordHashChange(ctx, s, f, "newhash", "snap-1", "edit", map[string]any{
		"bytes_appended": 42,
	})
	if err != nil {
		t.Fatalf("RecordHashChange: %v", err)
	}

	// UPDATE landed.
	row := s.DB().QueryRowContext(ctx, `SELECT last_hash FROM tracked_files WHERE id = ?`, id)
	var got string
	if err := row.Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != "newhash" {
		t.Errorf("last_hash = %q, want newhash", got)
	}

	// Audit row written with merged extra fields.
	lines := readAuditLines(t, auditPath)
	if len(lines) != 1 {
		t.Fatalf("got %d audit lines, want 1", len(lines))
	}
	got1 := lines[0]
	if got1["action"] != "edit" {
		t.Errorf("action = %v, want edit", got1["action"])
	}
	if got1["display_path"] != "~/x" {
		t.Errorf("display_path = %v", got1["display_path"])
	}
	if got1["snapshot_id"] != "snap-1" {
		t.Errorf("snapshot_id = %v", got1["snapshot_id"])
	}
	if got1["old_hash"] != "oldhash" || got1["new_hash"] != "newhash" {
		t.Errorf("hash fields = %v/%v", got1["old_hash"], got1["new_hash"])
	}
	if got1["bytes_appended"].(float64) != 42 {
		t.Errorf("bytes_appended = %v", got1["bytes_appended"])
	}
}

func TestRecordHashChange_EmptyActionSkipsAudit(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	auditPath := setupAuditLogger(t, dir)

	ctx := context.Background()

	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (path, display_path, added_at, last_hash) VALUES (?, ?, ?, ?)`,
		"/tmp/y", "~/y", time.Now().UTC().Format(time.RFC3339), "h0")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()
	f := File{ID: id, Path: "/tmp/y", DisplayPath: "~/y", LastHash: "h0"}

	if err := RecordHashChange(ctx, s, f, "h1", "snap-2", "", nil); err != nil {
		t.Fatalf("RecordHashChange: %v", err)
	}

	// UPDATE landed.
	row := s.DB().QueryRowContext(ctx, `SELECT last_hash FROM tracked_files WHERE id = ?`, id)
	var got string
	if err := row.Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != "h1" {
		t.Errorf("last_hash = %q, want h1", got)
	}

	// Audit file should not exist or be empty.
	if data, err := os.ReadFile(auditPath); err == nil && len(data) > 0 {
		t.Errorf("expected no audit output, got: %s", string(data))
	}
}

func TestRecordHashChange_ExtraOverridesCanonical(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	auditPath := setupAuditLogger(t, dir)

	ctx := context.Background()

	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (path, display_path, added_at, last_hash) VALUES (?, ?, ?, ?)`,
		"/tmp/z", "~/z", time.Now().UTC().Format(time.RFC3339), "h0")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()
	f := File{ID: id, Path: "/tmp/z", DisplayPath: "~/z", LastHash: "h0"}

	err = RecordHashChange(ctx, s, f, "h1", "snap-3", "edit", map[string]any{
		"display_path": "override",
	})
	if err != nil {
		t.Fatalf("RecordHashChange: %v", err)
	}

	lines := readAuditLines(t, auditPath)
	if len(lines) != 1 {
		t.Fatalf("got %d audit lines", len(lines))
	}
	if lines[0]["display_path"] != "override" {
		t.Errorf("display_path = %v, want override", lines[0]["display_path"])
	}
}
