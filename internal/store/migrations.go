package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationsDir = "migrations"

// configureGoose installs package-level goose settings: embedded FS,
// dialect, and (critically) our dlog-backed logger adapter so goose's
// own stdlib-log-based output is suppressed at default log levels.
//
// goose.SetLogger is package-level state, so this is effectively
// process-global; calling it on every invocation is harmless and
// keeps the wiring local to the only place that touches goose.
func configureGoose(ctx context.Context) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}
	goose.SetLogger(newGooseLogger(ctx))
	return nil
}

// RunMigrations applies any pending goose "up" migrations using the
// embedded migrations directory. Safe to call repeatedly.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	if err := configureGoose(ctx); err != nil {
		return err
	}
	dlog.From(ctx).Debug("running goose", "command", "up")
	if err := goose.RunContext(ctx, "up", db, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// RunGoose drives goose directly with an arbitrary command (e.g. "up",
// "down", "status") against the embedded migrations directory. Used by
// the `migrate` subcommand.
func RunGoose(ctx context.Context, db *sql.DB, command string, args ...string) error {
	if err := configureGoose(ctx); err != nil {
		return err
	}
	return goose.RunContext(ctx, command, db, migrationsDir, args...)
}

// CurrentDBVersion returns the schema version goose has recorded in
// the target database. Returns 0 (and no error) for a freshly-created
// DB before any migrations have been applied.
func CurrentDBVersion(ctx context.Context, db *sql.DB) (int64, error) {
	if err := configureGoose(ctx); err != nil {
		return 0, err
	}
	return goose.GetDBVersionContext(ctx, db)
}

// CurrentDBVersionBefore opens cfg, reads the current goose schema
// version without applying any pending migrations, then closes the
// connection. Returns 0 (and no error) when the goose_db_version
// table does not yet exist — i.e. a truly fresh DB. Used by the
// `dfm migrate up` summary so it can distinguish a first-run apply
// (which PersistentPreRunE has already performed via store.New) from
// a genuinely idempotent re-run.
//
// This function must not mutate the database: it issues a read-only
// SELECT and never invokes goose's up/down machinery.
func CurrentDBVersionBefore(ctx context.Context, cfg *config.Config) (int64, error) {
	db, _, err := Open(ctx, cfg)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	// Read goose_db_version directly so we don't accidentally
	// invoke any goose code path that might create the table.
	var version sql.NullInt64
	row := db.QueryRowContext(ctx,
		`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`)
	if err := row.Scan(&version); err != nil {
		// Most likely: table doesn't exist yet (fresh DB). Treat
		// any read error as "no schema recorded yet" — callers
		// only use this signal to refine a UX message.
		return 0, nil
	}
	if !version.Valid {
		return 0, nil
	}
	return version.Int64, nil
}
