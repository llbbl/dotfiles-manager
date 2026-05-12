package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/llbbl/dotfiles-manager/internal/apply"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// newSuggestionsCmd builds the `dotfiles suggestions` command, which
// lists stored AI suggestions. --status filters by lifecycle state
// (pending|applied|rejected|stale|all, default pending), --file scopes
// to one tracked file, and --json emits the list as JSON.
func newSuggestionsCmd() *cobra.Command {
	var (
		status string
		file   string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "suggestions",
		Short: "List suggestions",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()

			switch status {
			case "", "pending", "applied", "rejected", "stale", "all":
			default:
				return fmt.Errorf("invalid --status %q (pending|applied|rejected|stale|all)", status)
			}
			if status == "" {
				status = apply.StatusPending
			}
			filter := status
			if status == "all" {
				filter = ""
			}

			s, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			var fileID int64
			if file != "" {
				canonical, _, rerr := tracker.Resolve(file)
				if rerr == nil {
					files, lerr := tracker.List(ctx, s)
					if lerr != nil {
						return lerr
					}
					for _, f := range files {
						if f.Path == canonical || f.DisplayPath == file {
							fileID = f.ID
							break
						}
					}
				}
				if fileID == 0 {
					return exitf(exitNotFound, "not tracked: %s", file)
				}
			}

			repo := apply.NewRepo(s)
			rows, err := repo.List(ctx, fileID, filter)
			if err != nil {
				return err
			}

			// Build display-path map for nice rendering.
			files, _ := tracker.List(ctx, s)
			dispByID := map[int64]string{}
			for _, f := range files {
				dispByID[f.ID] = f.DisplayPath
			}

			out := c.OutOrStdout()
			if asJSON {
				list := make([]map[string]any, 0, len(rows))
				for _, sg := range rows {
					list = append(list, map[string]any{
						"id":           sg.ID,
						"file_id":      sg.FileID,
						"display_path": dispByID[sg.FileID],
						"provider":     sg.Provider,
						"status":       sg.Status,
						"created_at":   sg.CreatedAt.Format("2006-01-02T15:04:05Z"),
					})
				}
				return jsonEncode(out, list)
			}
			if len(rows) == 0 {
				fmt.Fprintln(out, "no suggestions")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tCREATED\tFILE")
			for _, sg := range rows {
				id := sg.ID
				if len(id) > 10 {
					id = id[:10]
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					id, sg.Status,
					sg.CreatedAt.Format("2006-01-02 15:04:05"),
					dispByID[sg.FileID])
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status (pending|applied|rejected|stale|all). default: pending")
	cmd.Flags().StringVar(&file, "file", "", "filter by tracked file (path or display)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit as JSON")
	return cmd
}
