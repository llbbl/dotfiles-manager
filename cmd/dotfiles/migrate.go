package main

import (
	"fmt"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/spf13/cobra"
)

// newMigrateCmd builds the `dotfiles migrate` command group for managing
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
			return store.RunGoose(ctx, db, name)
		},
	}
}
