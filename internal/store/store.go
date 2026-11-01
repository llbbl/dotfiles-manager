// Package store opens the libSQL/Turso state database (local file or
// remote-with-replica), runs the embedded goose migrations on New, and
// exposes a thin handle for the rest of the app. Remote URLs require a
// TURSO_AUTH_TOKEN; only a small allowlist of URL parts is ever logged.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	turso "turso.tech/database/tursogo"
)

// Store is a handle around an open *sql.DB.
type Store struct {
	db *sql.DB
	// shared marks this Store as a non-owning reference: Close is a
	// no-op. See SharedRef.
	shared bool
}

// DB returns the underlying *sql.DB.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the underlying *sql.DB. If this Store is a shared
// reference (constructed via SharedRef), Close is a no-op — the
// original owner is responsible for the underlying connection.
func (s *Store) Close() error {
	if s.shared {
		return nil
	}
	return s.db.Close()
}

// SharedRef returns a non-owning view of this Store. Callers can
// safely Close() the returned reference without closing the
// underlying *sql.DB; the original Store retains ownership. Used by
// the root cobra command to share its store with subcommands so we
// only open the DB (and run migrations) once per process.
func (s *Store) SharedRef() *Store {
	return &Store{db: s.db, shared: true}
}

// New opens the state DB, pings it, and runs pending migrations.
func New(ctx context.Context, cfg *config.Config) (*Store, error) {
	dlog.From(ctx).Info("opening state store", "target", scrub(strings.TrimSpace(cfg.State.URL)))
	db, target, err := open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s: %w", scrub(target), err)
	}
	if err := RunMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Open opens the state DB without running migrations. Used by the migrate
// subcommand so it can drive goose directly.
func Open(ctx context.Context, cfg *config.Config) (*sql.DB, string, error) {
	db, target, err := open(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("ping %s: %w", scrub(target), err)
	}
	return db, target, nil
}

func open(ctx context.Context, cfg *config.Config) (*sql.DB, string, error) {
	raw := strings.TrimSpace(cfg.State.URL)
	if raw == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, "", fmt.Errorf("resolve home: %w", err)
		}
		raw = "file://" + filepath.Join(home, ".local", "share", "dotfiles", "state.db")
	}

	if isRemote(raw) {
		token := cfg.State.AuthToken
		if token == "" {
			return nil, "", errors.New("remote Turso URL requires an auth token; set TURSO_AUTH_TOKEN")
		}
		cachePath, err := remoteCachePath(raw)
		if err != nil {
			return nil, "", err
		}
		if err := ensureParentDir(cachePath); err != nil {
			return nil, "", err
		}
		syncDB, err := turso.NewTursoSyncDb(ctx, turso.TursoSyncDbConfig{
			Path:       cachePath,
			RemoteUrl:  raw,
			AuthToken:  token,
			ClientName: "dotfiles-manager",
		})
		if err != nil {
			return nil, "", fmt.Errorf("open remote %s: %w", scrub(raw), err)
		}
		db, err := syncDB.Connect(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("connect remote %s: %w", scrub(raw), err)
		}
		return db, raw, nil
	}

	path := localPath(raw)
	if err := ensureParentDir(path); err != nil {
		return nil, "", err
	}
	connector, err := turso.NewConnector(path)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", path, err)
	}
	return sql.OpenDB(fkConnector{connector}), path, nil
}

// fkConnector enables foreign keys on each new pooled connection: the pragma
// is per-connection, off by default in turso, and tursogo has no DSN option
// for it. The remote sync connector is unexported, so remote mode can't be wrapped.
type fkConnector struct{ driver.Connector }

func (c fkConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	ex, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("enable foreign keys: driver connection cannot exec")
	}
	if _, err := ex.ExecContext(ctx, "PRAGMA foreign_keys = ON", nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	return conn, nil
}

func isRemote(u string) bool {
	return strings.HasPrefix(u, "libsql://") ||
		strings.HasPrefix(u, "https://") ||
		strings.HasPrefix(u, "http://") ||
		strings.HasPrefix(u, "wss://") ||
		strings.HasPrefix(u, "ws://")
}

// localPath strips a file:// prefix and returns a filesystem path suitable
// for the tursogo driver.
func localPath(u string) string {
	if p, ok := strings.CutPrefix(u, "file://"); ok {
		return p
	}
	return u
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

// remoteCachePath returns the local replica cache file for a remote URL.
func remoteCachePath(remote string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	host := remote
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		host = u.Host
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, host)
	return filepath.Join(home, ".local", "share", "dotfiles", "remote-cache", safe+".db"), nil
}

// scrub redacts the auth-bearing parts of a URL for logging.
func scrub(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return u
	}
	return parsed.Scheme + "://" + parsed.Host
}
