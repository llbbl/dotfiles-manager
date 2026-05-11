package main

import (
	"errors"
	"fmt"

	"github.com/llbbl/dotfiles-manager/internal/apply"
	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/spf13/cobra"
)

func newRejectCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "reject <suggestion-id>",
		Short: "Reject a previously generated suggestion",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			id := args[0]

			s, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			repo := apply.NewRepo(s)
			sg, err := repo.Get(ctx, id)
			if err != nil {
				if errors.Is(err, apply.ErrNotFound) {
					return exitf(exitResolveErr, "suggestion not found: %s", id)
				}
				return err
			}

			fields := map[string]any{
				"suggestion_id": id,
				"file_id":       sg.FileID,
			}
			if file, ferr := repo.ResolveFile(ctx, id); ferr == nil {
				fields["display_path"] = file.DisplayPath
			}

			if err := repo.Reject(ctx, id); err != nil {
				if errors.Is(err, apply.ErrAlreadyDecided) {
					return exitf(exitAlreadyOrMiss,
						"suggestion %s already decided (status=%s)", id, sg.Status)
				}
				return err
			}
			audit.Log(ctx, "reject", fields)

			out := c.OutOrStdout()
			if asJSON {
				return jsonEncode(out, map[string]any{"id": id, "status": apply.StatusRejected})
			}
			fmt.Fprintf(out, "rejected %s\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit result as JSON")
	return cmd
}
