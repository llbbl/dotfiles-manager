package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// newListCmd builds the `dotfiles list` command, which prints every
// tracked file with its added-at date and short content hash. --json
// emits the listing as JSON.
func newListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tracked files",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			files, err := tracker.List(c.Context(), s)
			if err != nil {
				return err
			}

			if asJSON {
				out := make([]map[string]any, 0, len(files))
				for _, f := range files {
					entry := map[string]any{
						"id":           f.ID,
						"path":         f.Path,
						"display_path": f.DisplayPath,
						"added_at":     f.AddedAt.Format(time.RFC3339),
						"last_hash":    f.LastHash,
					}
					if !f.LastSynced.IsZero() {
						entry["last_synced"] = f.LastSynced.Format(time.RFC3339)
					}
					out = append(out, entry)
				}
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if len(files) == 0 {
				fmt.Fprintln(c.OutOrStdout(),
					"no tracked files. use `dotfiles track <path>` to add one.")
				return nil
			}

			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "FILE\tADDED\tLAST HASH")
			for _, f := range files {
				added := f.AddedAt.Format("2006-01-02")
				hash := f.LastHash
				if hash == "" {
					hash = "(none)"
				} else if len(hash) > 8 {
					hash = hash[:8]
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", f.DisplayPath, added, hash)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit list as JSON")
	return cmd
}
