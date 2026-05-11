package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status [<path>]",
		Short: "Show local vs backed-up diff",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			var reports []tracker.StatusReport
			if len(args) == 1 {
				r, err := tracker.ComputeStatusOne(c.Context(), s, args[0])
				if err != nil {
					if errors.Is(err, tracker.ErrNotTracked) {
						fmt.Fprintf(c.ErrOrStderr(), "not tracked: %s\n", args[0])
						os.Exit(exitAlreadyOrMiss)
					}
					return err
				}
				reports = []tracker.StatusReport{r}
			} else {
				rs, err := tracker.ComputeStatus(c.Context(), s)
				if err != nil {
					return err
				}
				reports = rs
			}

			if asJSON {
				out := make([]map[string]any, 0, len(reports))
				for _, r := range reports {
					entry := map[string]any{
						"path":         r.File.Path,
						"display_path": r.File.DisplayPath,
						"status":       string(r.Status),
						"hash":         r.Hash,
						"last_hash":    r.File.LastHash,
						"added_at":     r.File.AddedAt.Format(time.RFC3339),
					}
					out = append(out, entry)
				}
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(out); err != nil {
					return err
				}
			} else if len(reports) == 0 {
				fmt.Fprintln(c.OutOrStdout(),
					"no tracked files. use `dotfiles track <path>` to add one.")
				return nil
			} else {
				tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "STATUS\tFILE")
				for _, r := range reports {
					fmt.Fprintf(tw, "%s\t%s\n", r.Status, r.File.DisplayPath)
				}
				tw.Flush()
			}

			allClean := true
			for _, r := range reports {
				if r.Status != tracker.StatusClean {
					allClean = false
					break
				}
			}
			if !allClean {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit status as JSON")
	return cmd
}
