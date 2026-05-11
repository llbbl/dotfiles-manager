package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/config"
	turso "turso.tech/database/tursogo"
)

type Store struct {
	db     *sql.DB
	target string
}

func (s *Store) DB() *sql.DB     { return s.db }
func (s *Store) Target() string  { return s.target }
func (s *Store) Close() error    { return s.db.Close() }

// New opens the state DB, pings it, and runs pending migrations.
func New(ctx context.Context, cfg *config.Config) (*Store, error) {
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
	return &Store{db: db, target: target}, nil
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
	db, err := sql.Open("turso", path)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", path, err)
	}
	return db, path, nil
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
	if strings.HasPrefix(u, "file://") {
		return strings.TrimPrefix(u, "file://")
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
