package main

import "github.com/spf13/cobra"

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage state database migrations",
	}
	cmd.AddCommand(
		stubCmd("status", "Show migration status"),
		stubCmd("up", "Apply pending migrations"),
		stubCmd("down", "Roll back the last migration"),
	)
	return cmd
}
