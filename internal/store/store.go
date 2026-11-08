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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	turso "turso.tech/database/tursogo"
)

// Remote pulls and final pushes are bounded to 30 seconds.
var remoteSyncTimeout = 30 * time.Second

// Store is a handle around an open *sql.DB.
type Store struct {
	db                  *sql.DB
	sync                *turso.TursoSyncDb
	push                func(context.Context) error
	target              string
	preMigrationVersion int64
	closeOnce           sync.Once
	closeErr            error
	// shared marks this Store as a non-owning reference: Close is a
	// no-op. See SharedRef.
	shared bool
}

// DB returns the underlying *sql.DB.
func (s *Store) DB() *sql.DB { return s.db }

// PreMigrationVersion returns the schema version read before New ran migrations.
// A fresh database has version 0; -1 means the version could not be read.
func (s *Store) PreMigrationVersion() int64 { return s.preMigrationVersion }

// Close pushes remote changes before closing the database, even if the push fails.
// Shared references leave both operations to their owner.
func (s *Store) Close() error {
	if s.shared {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.push != nil {
			ctx, cancel := context.WithTimeout(context.Background(), remoteSyncTimeout)
			defer cancel()
			if err := s.push(ctx); err != nil {
				s.closeErr = fmt.Errorf("changes saved locally but not pushed to %s: %w; they push on the next command that completes successfully",
					scrub(s.target), &remoteOperationError{cause: err})
			}
		}
		closeErr := s.db.Close()
		if closeErr != nil && s.sync != nil {
			closeErr = &remoteOperationError{cause: closeErr}
		}
		s.closeErr = errors.Join(s.closeErr, closeErr)
	})
	return s.closeErr
}

// SharedRef returns a non-owning view of this Store. Callers can
// safely Close() the returned reference without closing the
// underlying *sql.DB; the original Store retains ownership. Used by
// the root cobra command to share its store with subcommands so we
// only open the DB (and run migrations) once per process.
func (s *Store) SharedRef() *Store {
	return &Store{db: s.db, shared: true, preMigrationVersion: s.preMigrationVersion}
}

// New opens the state DB, pings it, and runs pending migrations.
func New(ctx context.Context, cfg *config.Config) (*Store, error) {
	dlog.From(ctx).Info("opening state store", "target", scrub(strings.TrimSpace(cfg.State.URL)))
	s, _, err := Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.preMigrationVersion = readPreMigrationVersion(ctx, s.db)
	if err := RunMigrations(ctx, s.db); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	return s, nil
}

// Open opens the state DB without running migrations. Used by the migrate
// subcommand so it can drive goose directly.
func Open(ctx context.Context, cfg *config.Config) (*Store, string, error) {
	s, target, err := open(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	if err := s.db.PingContext(ctx); err != nil {
		_ = s.db.Close()
		if s.sync != nil {
			err = &remoteOperationError{cause: err}
		}
		return nil, "", fmt.Errorf("ping %s: %w", scrub(target), err)
	}
	return s, scrub(target), nil
}

func open(ctx context.Context, cfg *config.Config) (*Store, string, error) {
	if err := ensureNativeLib(); err != nil {
		return nil, "", err
	}
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
		bootCtx, cancelBoot := context.WithTimeout(ctx, remoteSyncTimeout)
		defer cancelBoot()
		syncDB, err := turso.NewTursoSyncDb(bootCtx, turso.TursoSyncDbConfig{
			Path:       cachePath,
			RemoteUrl:  raw,
			AuthToken:  token,
			ClientName: "dotfiles-manager",
		})
		if err != nil {
			return nil, "", fmt.Errorf("open remote %s: %w", scrub(raw), &remoteOperationError{cause: err})
		}
		db, err := syncDB.Connect(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("connect remote %s: %w", scrub(raw), &remoteOperationError{cause: err})
		}
		pullCtx, cancel := context.WithTimeout(ctx, remoteSyncTimeout)
		defer cancel()
		if _, err := syncDB.Pull(pullCtx); err != nil {
			// A pull abandoned at its deadline leaves the sync handle unable to connect, so
			// only a pull that failed on its own (for example offline) falls back to the replica.
			if errors.Is(err, context.DeadlineExceeded) {
				_ = db.Close()
				return nil, "", fmt.Errorf("pull from %s: %w", scrub(raw), &remoteOperationError{cause: err})
			}
			fmt.Fprintf(os.Stderr, "warning: could not pull from %s: %v; using local replica\n",
				scrub(raw), &remoteOperationError{cause: err})
		}
		return &Store{db: db, sync: syncDB, push: syncDB.Push, target: scrub(raw), preMigrationVersion: -1}, raw, nil
	}

	path := localPath(raw)
	if err := ensureParentDir(path); err != nil {
		return nil, "", err
	}
	connector, err := turso.NewConnector(path)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", path, err)
	}
	return &Store{db: sql.OpenDB(fkConnector{connector}), target: path, preMigrationVersion: -1}, path, nil
}

// Driver errors can contain credentials or row contents; preserve their identity
// for errors.Is/As while keeping their text out of diagnostics.
type remoteOperationError struct{ cause error }

// Only a complete 4xx/5xx token after "status" or "HTTP" counts, so numbers from
// row data or byte counts (401.75, 401,250, 123-456) are never reported as a status.
var remoteHTTPStatus = regexp.MustCompile(`(?i)(?:\bstatus(?:\s+code)?\s*[:=]\s*|\bhttp(?:/\d(?:\.\d)?)?\s+(?:status(?:\s+code)?\s*:?\s*)?)([45][0-9]{2})(?:$|[\s);\]])`)

func (e *remoteOperationError) Error() string {
	if status := remoteHTTPStatus.FindStringSubmatch(e.cause.Error()); len(status) == 2 {
		return "remote operation failed (HTTP " + status[1] + ")"
	}
	var networkErr net.Error
	if errors.Is(e.cause, context.DeadlineExceeded) || errors.As(e.cause, &networkErr) {
		return "remote operation failed (network unreachable/timeout)"
	}
	return "remote operation failed"
}
func (e *remoteOperationError) Unwrap() error { return e.cause }

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
	u, err := url.Parse(remote)
	if err != nil || u.Host == "" {
		return "", errors.New("invalid remote URL")
	}
	host := u.Host
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
		if isRemote(u) {
			return "<redacted remote URL>"
		}
		return u
	}
	return parsed.Scheme + "://" + parsed.Host
}
