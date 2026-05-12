package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// newUntrackCmd builds the `dfm untrack` command, which removes a
// file from active management. The file itself stays on disk and its
// snapshots remain in the store for later restore.
func newUntrackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "untrack <path>",
		Short: "Stop managing a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			f, err := tracker.Untrack(c.Context(), s, args[0])
			if err != nil {
				if errors.Is(err, tracker.ErrNotTracked) {
					fmt.Fprintf(c.ErrOrStderr(), "not tracked: %s\n", args[0])
					os.Exit(exitAlreadyOrMiss)
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "untracked %s\n", f.DisplayPath)
			return nil
		},
	}
}
