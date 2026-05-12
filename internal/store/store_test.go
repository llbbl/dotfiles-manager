package store

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func TestNew_EmbeddedFile_RunsMigrationsAndIsAlive(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")

	cfg := &config.Config{State: config.StateConfig{URL: "file://" + dbPath}}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	want := map[string]bool{
		"tracked_files": false,
		"suggestions":   false,
		"actions":       false,
	}
	rows, err := s.DB().QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, name)
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	_ = rows.Close()

	for tbl, ok := range want {
		if !ok {
			sort.Strings(names)
			t.Errorf("missing table %q; saw %v", tbl, names)
		}
	}

	_, err = s.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (path, display_path, added_at) VALUES (?, ?, ?)`,
		"/abs/path/.zshrc", "~/.zshrc", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var got string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT display_path FROM tracked_files WHERE path = ?`, "/abs/path/.zshrc").
		Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "~/.zshrc" {
		t.Errorf("display_path = %q, want %q", got, "~/.zshrc")
	}
}

func TestNew_RemoteURLWithoutTokenFails(t *testing.T) {
	cfg := &config.Config{State: config.StateConfig{URL: "libsql://example.turso.io"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := New(ctx, cfg); err == nil {
		t.Fatal("expected error for remote URL without auth token")
	}
}
