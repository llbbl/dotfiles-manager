package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

func newTestLogger(t *testing.T) (*Logger, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "subdir", "actions.jsonl")
	cfg := &config.Config{
		State: config.StateConfig{URL: "file://" + filepath.Join(dir, "state.db")},
		Log:   config.LogConfig{Path: logPath},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	l, err := New(ctx, cfg, s)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, s, logPath
}

func TestNew_CreatesParentDirAndFileWithPerms(t *testing.T) {
	l, _, path := newTestLogger(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 0700", dirInfo.Mode().Perm())
	}
	_ = l
}

func TestLog_WritesJSONLAndActionsRow(t *testing.T) {
	l, s, path := newTestLogger(t)
	ctx := context.Background()
	if err := l.Log(ctx, "track", map[string]any{"path": "/tmp/foo", "hash": "deadbeef"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("jsonl record not newline-terminated")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(string(data), "\n")), &rec); err != nil {
		t.Fatalf("json: %v", err)
	}
	if rec["action"] != "track" {
		t.Errorf("action = %v", rec["action"])
	}
	if rec["path"] != "/tmp/foo" {
		t.Errorf("path = %v", rec["path"])
	}
	if _, ok := rec["ts"].(string); !ok {
		t.Errorf("ts missing or wrong type")
	}

	var count int
	row := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE action = ?`, "track")
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("actions count = %d, want 1", count)
	}
}

func TestLog_ReservedFieldsOverwriteCaller(t *testing.T) {
	l, _, path := newTestLogger(t)
	ctx := context.Background()
	if err := l.Log(ctx, "sync", map[string]any{"ts": "BOGUS", "action": "BOGUS"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	data, _ := os.ReadFile(path)
	var rec map[string]any
	_ = json.Unmarshal([]byte(strings.TrimRight(string(data), "\n")), &rec)
	if rec["action"] != "sync" {
		t.Errorf("action = %v, want sync", rec["action"])
	}
	if rec["ts"] == "BOGUS" {
		t.Errorf("ts not overwritten")
	}
}

func TestClose_Idempotent(t *testing.T) {
	l, _, _ := newTestLogger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestPackageLog_NoDefaultIsNoOp(t *testing.T) {
	SetDefault(nil)
	Log(context.Background(), "test", nil)
}

func TestLog_BackendRouting(t *testing.T) {
	cases := []struct {
		name        string
		backend     string
		wantJSONL   bool
		wantDBCount int
	}{
		{"both", "both", true, 1},
		{"jsonl", "jsonl", true, 0},
		{"db", "db", false, 1},
		{"none", "none", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "subdir", "actions.jsonl")
			cfg := &config.Config{
				State: config.StateConfig{URL: "file://" + filepath.Join(dir, "state.db")},
				Log:   config.LogConfig{Path: logPath, Backend: tc.backend},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			s, err := store.New(ctx, cfg)
			if err != nil {
				t.Fatalf("store.New: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })

			l, err := New(ctx, cfg, s)
			if err != nil {
				t.Fatalf("audit.New: %v", err)
			}
			t.Cleanup(func() { _ = l.Close() })

			if err := l.Log(ctx, "track", map[string]any{"path": "/tmp/foo"}); err != nil {
				t.Fatalf("Log: %v", err)
			}

			_, statErr := os.Stat(logPath)
			fileExists := statErr == nil
			if fileExists != tc.wantJSONL {
				t.Errorf("jsonl file exists = %v, want %v (stat err: %v)", fileExists, tc.wantJSONL, statErr)
			}

			var count int
			row := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE action = ?`, "track")
			if err := row.Scan(&count); err != nil {
				t.Fatalf("count: %v", err)
			}
			if count != tc.wantDBCount {
				t.Errorf("actions count = %d, want %d", count, tc.wantDBCount)
			}
		})
	}
}

func TestNew_BothRequiresStore(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Log: config.LogConfig{Path: filepath.Join(dir, "a.jsonl"), Backend: "both"},
	}
	if _, err := New(context.Background(), cfg, nil); err == nil {
		t.Fatal("expected error for backend=both with nil store")
	}
}

func TestNew_JSONLAllowsNilStore(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "a.jsonl")
	cfg := &config.Config{
		Log: config.LogConfig{Path: logPath, Backend: "jsonl"},
	}
	l, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if err := l.Log(context.Background(), "track", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("expected jsonl file to exist: %v", err)
	}
}

func TestLog_AfterCloseReturnsError(t *testing.T) {
	for _, backend := range []string{"both", "jsonl", "db", "none"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.Config{
				State: config.StateConfig{URL: "file://" + filepath.Join(dir, "state.db")},
				Log:   config.LogConfig{Path: filepath.Join(dir, "a.jsonl"), Backend: backend},
			}
			ctx := context.Background()
			s, err := store.New(ctx, cfg)
			if err != nil {
				t.Fatalf("store.New: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })

			l, err := New(ctx, cfg, s)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := l.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := l.Log(ctx, "track", nil); err == nil {
				t.Errorf("backend=%s: expected error logging after Close", backend)
			}
		})
	}
}
