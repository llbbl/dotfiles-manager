package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
		`INSERT INTO tracked_files (id, path, display_path, added_at) VALUES (?, ?, ?, ?)`,
		"file-1", "/abs/path/.zshrc", "~/.zshrc", time.Now().UTC().Format(time.RFC3339))
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

func TestOpen_LSMPrecedence(t *testing.T) {
	for _, envToken := range []bool{false, true} {
		name := "reference"
		if envToken {
			name = "environment"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			t.Setenv("TURSO_AUTH_TOKEN", "")
			if envToken {
				t.Setenv("TURSO_AUTH_TOKEN", "SECRET-110")
			}
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			log := filepath.Join(dir, "args")
			t.Setenv("STUB_ARGS", log)
			script := `#!/bin/sh
printf '%s\n' "$@" >> "$STUB_ARGS"
printf 'LEAK-STDERR' >&2
printf 'SECRET-110'
`
			if err := os.WriteFile(filepath.Join(dir, "lsm"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			cfg, err := config.Load("")
			if err != nil {
				t.Fatal(err)
			}
			cfg.State.URL = server.URL
			cfg.State.Secret = &config.SecretRef{Provider: "lsm", App: "dfm", Env: "prod"}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, _, err := open(ctx, cfg)
			if s != nil {
				_ = s.Close()
			}
			if err == nil || strings.Contains(err.Error(), "requires an auth token") {
				t.Fatal("did not reach remote connection after resolving token")
			}
			if strings.Contains(err.Error(), "SECRET-110") || strings.Contains(err.Error(), "LEAK-") {
				t.Error("sensitive output reached store error")
			}
			args, readErr := os.ReadFile(log)
			if envToken {
				if !os.IsNotExist(readErr) {
					t.Error("environment token still invoked lsm")
				}
			} else if readErr != nil || string(args) != "get\n--app\ndfm\n--env\nprod\nTURSO_AUTH_TOKEN\n" {
				t.Error("reference did not invoke expected lookup")
			}
		})
	}
}
