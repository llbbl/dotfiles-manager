// Package audit is the append-only action log. Each Log call writes a
// newline-terminated JSONL record to the configured log file and/or
// inserts a corresponding row into the libSQL actions table, based on
// cfg.Log.Backend ("both" | "jsonl" | "db" | "none"). The JSONL file is
// the durable ground truth; libSQL is the queryable index.
//
// Callers are responsible for masking sensitive values BEFORE passing
// them in. Audit does not scan field values for secrets, and never
// marshals a *config.Config directly.
//
// Canonical event names (action strings) used in this project:
//
//	track, untrack, edit, append, backup, restore, prune,
//	sync, sync.file, init, scan, suggest, apply, apply_failed, reject,
//	snapshot.created, snapshot.restored, snapshot.pruned.
//
// Privacy rules (see docs/architecture.md): never pass secrets, prompts,
// AI responses, diff bodies, or file contents through this package — log
// paths, IDs, hashes, counts, and durations only.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// Logger writes audit events to a JSONL file and/or libSQL according to
// cfg.Log.Backend.
type Logger struct {
	mu      sync.Mutex
	file    *os.File
	store   *store.Store
	path    string
	backend string
	closed  bool
}

// New constructs a Logger honoring cfg.Log.Backend:
//
//   - "both"  — opens the JSONL file and requires a non-nil store.
//   - "jsonl" — opens the JSONL file; the store may be nil.
//   - "db"    — does not touch the filesystem; requires a non-nil store.
//   - "none"  — silent stub; opens nothing, accepts nil store.
//
// When a file is opened it is created with mode 0600 and any missing
// parent directories are created with mode 0700.
func New(ctx context.Context, cfg *config.Config, s *store.Store) (*Logger, error) {
	if cfg == nil {
		return nil, errors.New("audit: nil config")
	}
	backend := cfg.Log.Backend
	if backend == "" {
		backend = "both"
	}
	_ = ctx

	l := &Logger{store: s, backend: backend}

	switch backend {
	case "none":
		return l, nil
	case "db":
		if s == nil {
			return nil, errors.New("audit: backend=db requires a non-nil store")
		}
		return l, nil
	case "jsonl", "both":
		if backend == "both" && s == nil {
			return nil, errors.New("audit: backend=both requires a non-nil store")
		}
		path := cfg.Log.Path
		if path == "" {
			return nil, errors.New("audit: empty log path")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("audit: mkdir log dir: %w", err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("audit: open log: %w", err)
		}
		l.file = f
		l.path = path
		return l, nil
	default:
		return nil, fmt.Errorf("audit: unknown backend %q", backend)
	}
}

// Path returns the JSONL file path (empty when the backend does not
// write to a file).
func (l *Logger) Path() string { return l.path }

// Log appends a JSONL record and/or inserts a row into libSQL according
// to the configured backend. Reserved keys ts and action overwrite any
// caller-supplied values.
func (l *Logger) Log(ctx context.Context, action string, fields map[string]any) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("audit: logger closed")
	}
	if l.backend == "none" {
		return nil
	}

	now := time.Now().UTC()
	ts := now.Format("2006-01-02T15:04:05.000Z")

	writeJSONL := l.backend == "both" || l.backend == "jsonl"
	writeDB := l.backend == "both" || l.backend == "db"

	if writeJSONL {
		if l.file == nil {
			return errors.New("audit: logger closed")
		}
		record := make(map[string]any, len(fields)+2)
		maps.Copy(record, fields)
		record["ts"] = ts
		record["action"] = action
		line, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("audit: marshal: %w", err)
		}
		line = append(line, '\n')
		if _, err := l.file.Write(line); err != nil {
			return fmt.Errorf("audit: write jsonl: %w", err)
		}
	}

	if writeDB && l.store != nil {
		payload := make(map[string]any, len(fields))
		maps.Copy(payload, fields)
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("audit: marshal payload: %w", err)
		}
		if _, err := l.store.DB().ExecContext(ctx,
			`INSERT INTO actions (ts, action, payload_json) VALUES (?, ?, ?)`,
			ts, action, string(payloadJSON),
		); err != nil {
			return fmt.Errorf("audit: insert action: %w", err)
		}
	}
	return nil
}

// Close flushes and closes the file handle. Safe to call multiple times.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.file == nil {
		l.closed = true
		l.file = nil
		return nil
	}
	err := l.file.Close()
	l.file = nil
	l.closed = true
	return err
}

var (
	defaultMu sync.RWMutex
	def       *Logger
)

// SetDefault registers a process-wide Logger for the package-level Log
// convenience. Passing nil clears the default.
func SetDefault(l *Logger) {
	defaultMu.Lock()
	def = l
	defaultMu.Unlock()
}

// Default returns the currently registered Logger (may be nil).
func Default() *Logger {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return def
}

// Log routes to the default Logger if one is set, otherwise no-ops. Any
// error from the default Logger is silently dropped — callers that need
// the error must hold a *Logger and invoke (*Logger).Log directly.
func Log(ctx context.Context, action string, fields map[string]any) {
	defaultMu.RLock()
	l := def
	defaultMu.RUnlock()
	if l == nil {
		return
	}
	_ = l.Log(ctx, action, fields)
}
