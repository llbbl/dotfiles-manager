package store

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
)

// openTestDB opens a fresh file-backed libSQL DB without running
// migrations, so individual tests can drive goose directly.
func openTestDB(t *testing.T) (context.Context, *sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + dbPath}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	db, _, err := Open(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("Open: %v", err)
	}
	return ctx, db, func() {
		_ = db.Close()
		cancel()
	}
}

// safeBuffer is a goroutine-safe wrapper around bytes.Buffer. slog
// handlers may be called from goose's internal goroutines, so we
// guard writes for the race detector.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestRunMigrations_DefaultLevel_NoGooseOutput(t *testing.T) {
	_, db, cleanup := openTestDB(t)
	defer cleanup()

	// dlog at error level — goose's Printf-routed-to-debug messages
	// should not appear.
	var buf safeBuffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})
	ctx := dlog.Into(context.Background(), slog.New(h))

	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	// Run again to provoke the "no migrations to run" path goose
	// would otherwise stdlib-log.
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("RunMigrations (idempotent): %v", err)
	}

	got := buf.String()
	if strings.Contains(got, "goose:") {
		t.Errorf("default level dlog should not surface goose lines, got:\n%s", got)
	}
}

func TestRunMigrations_DebugLevel_RoutesGooseToDlog(t *testing.T) {
	_, db, cleanup := openTestDB(t)
	defer cleanup()

	var buf safeBuffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	ctx := dlog.Into(context.Background(), slog.New(h))

	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	// Second invocation exercises the "no migrations to run" branch
	// that historically printed via the stdlib logger.
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("RunMigrations (idempotent): %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "goose:") {
		t.Errorf("expected goose: lines at debug level, got:\n%s", got)
	}
}

func TestCurrentDBVersion_AfterMigrations(t *testing.T) {
	ctx, db, cleanup := openTestDB(t)
	defer cleanup()

	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	v, err := CurrentDBVersion(ctx, db)
	if err != nil {
		t.Fatalf("CurrentDBVersion: %v", err)
	}
	if v < 1 {
		t.Errorf("version = %d, want >= 1", v)
	}
}
