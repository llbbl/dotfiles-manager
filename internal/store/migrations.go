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

func configureGoose() error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}
	return nil
}

// RunMigrations applies any pending goose "up" migrations using the
// embedded migrations directory. Safe to call repeatedly.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	if err := configureGoose(); err != nil {
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
	if err := configureGoose(); err != nil {
		return err
	}
	return goose.RunContext(ctx, command, db, migrationsDir, args...)
}
