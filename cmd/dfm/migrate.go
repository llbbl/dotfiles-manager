package main

import (
	"database/sql"
	"fmt"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/spf13/cobra"
)

// newMigrateCmd builds the `dfm migrate` command group for managing
// state-database schema migrations. Subcommands are status, up, down,
// and redo, each delegating to goose against the configured store.
func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage state database migrations",
	}
	cmd.AddCommand(
		migrateSubCmd("status", "Show migration status"),
		migrateSubCmd("up", "Apply pending migrations"),
		migrateSubCmd("down", "Roll back the last migration"),
		migrateSubCmd("redo", "Re-run the last migration (down then up)"),
	)
	return cmd
}

func migrateSubCmd(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			cfg := config.FromContext(ctx)
			if cfg == nil {
				return fmt.Errorf("config not loaded")
			}
			db, target, err := store.Open(ctx, cfg)
			if err != nil {
				return err
			}
			defer db.Close()
			if flagVerbose {
				fmt.Fprintf(c.ErrOrStderr(), "state: %s\n", target)
			}

			// Capture before/after versions to print a human-readable
			// summary that replaces the goose-emitted "no migrations
			// to run" line that's now routed through dlog at debug.
			var before int64
			if name == "up" || name == "down" || name == "redo" {
				before, _ = store.CurrentDBVersion(ctx, db)
			}
			if err := store.RunGoose(ctx, db, name); err != nil {
				return err
			}
			printMigrateSummary(c, db, name, before)
			return nil
		},
	}
}

// printMigrateSummary emits a one-line human-readable summary after a
// successful goose command, so that operators get feedback even though
// goose's own output is suppressed at default log level.
func printMigrateSummary(c *cobra.Command, db *sql.DB, name string, before int64) {
	ctx := c.Context()
	out := c.OutOrStdout()
	switch name {
	case "up":
		after, err := store.CurrentDBVersion(ctx, db)
		if err != nil {
			fmt.Fprintf(out, "dfm migrate: up complete (current version unknown: %v)\n", err)
			return
		}
		if after == before {
			// PersistentPreRunE's store.New already auto-applies
			// pending migrations, so by the time we land here
			// before == after even on a fresh DB. Use the version
			// peeked BEFORE that auto-migrate to distinguish a
			// first-run apply from a genuinely idempotent re-run.
			if preMigrationVersion >= 0 && preMigrationVersion < after {
				applied := after - preMigrationVersion
				fmt.Fprintf(out, "dfm migrate: initial migration applied — schema is at version %d (%d applied)\n", after, applied)
				return
			}
			fmt.Fprintf(out, "dfm migrate: already at version %d — nothing to do\n", after)
			return
		}
		fmt.Fprintf(out, "dfm migrate: migrated to version %d (%d applied)\n", after, after-before)
	case "down":
		after, err := store.CurrentDBVersion(ctx, db)
		if err != nil {
			fmt.Fprintf(out, "dfm migrate: down complete (current version unknown: %v)\n", err)
			return
		}
		fmt.Fprintf(out, "dfm migrate: rolled back to version %d\n", after)
	case "redo":
		after, err := store.CurrentDBVersion(ctx, db)
		if err != nil {
			fmt.Fprintf(out, "dfm migrate: redo complete (current version unknown: %v)\n", err)
			return
		}
		fmt.Fprintf(out, "dfm migrate: redo complete at version %d\n", after)
	case "status":
		after, err := store.CurrentDBVersion(ctx, db)
		if err != nil {
			fmt.Fprintf(out, "dfm migrate: status complete (current version unknown: %v)\n", err)
			return
		}
		fmt.Fprintf(out, "dfm migrate: current version %d\n", after)
	}
}
