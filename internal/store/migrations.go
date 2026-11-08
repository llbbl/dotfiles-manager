package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

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

func readPreMigrationVersion(ctx context.Context, db *sql.DB) int64 {
	// A missing version table is a fresh database (version 0), not a read failure.
	var exists int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`).Scan(&exists); err != nil {
		return -1
	}
	if exists == 0 {
		return 0
	}
	var version sql.NullInt64
	row := db.QueryRowContext(ctx,
		`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`)
	if err := row.Scan(&version); err != nil {
		return -1
	}
	if !version.Valid {
		return 0
	}
	return version.Int64
}
