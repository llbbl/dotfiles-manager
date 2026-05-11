package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/vcs"
	"github.com/spf13/cobra"
)

type logEntry struct {
	Time    time.Time
	Action  string
	Subject string
	Source  string
	Payload map[string]any
}

func newLogCmd() *cobra.Command {
	var (
		sinceFlag    string
		limit        int
		asJSON       bool
		withCommits  bool
	)
	cmd := &cobra.Command{
		Use:   "log [<file>]",
		Short: "Show change history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			cfg := config.FromContext(ctx)

			s, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			var sinceTS string
			if sinceFlag != "" {
				if t, err := time.Parse("2006-01-02", sinceFlag); err == nil {
					sinceTS = t.UTC().Format(time.RFC3339)
				} else if t, err := time.Parse(time.RFC3339, sinceFlag); err == nil {
					sinceTS = t.UTC().Format(time.RFC3339)
				} else {
					return fmt.Errorf("invalid --since %q (YYYY-MM-DD or RFC3339)", sinceFlag)
				}
			}

			query := `SELECT ts, action, payload_json FROM actions`
			var conds []string
			var argsQ []any
			if sinceTS != "" {
				conds = append(conds, "ts >= ?")
				argsQ = append(argsQ, sinceTS)
			}
			if len(args) == 1 {
				conds = append(conds, "json_extract(payload_json, '$.path') = ? OR json_extract(payload_json, '$.display_path') = ?")
				argsQ = append(argsQ, args[0], args[0])
			}
			if len(conds) > 0 {
				query += " WHERE " + strings.Join(conds, " AND ")
			}
			query += " ORDER BY ts DESC"
			if limit > 0 {
				query += fmt.Sprintf(" LIMIT %d", limit)
			}

			rows, err := s.DB().QueryContext(ctx, query, argsQ...)
			if err != nil {
				return err
			}
			defer rows.Close()

			var entries []logEntry
			for rows.Next() {
				var ts, action, payload string
				if err := rows.Scan(&ts, &action, &payload); err != nil {
					return err
				}
				var pl map[string]any
				_ = json.Unmarshal([]byte(payload), &pl)
				t, _ := time.Parse(time.RFC3339, ts)
				if t.IsZero() {
					t, _ = time.Parse("2006-01-02T15:04:05.000Z", ts)
				}
				entries = append(entries, logEntry{
					Time:    t,
					Action:  action,
					Subject: subjectFrom(pl),
					Source:  "db",
					Payload: pl,
				})
			}
			if err := rows.Err(); err != nil && err != sql.ErrNoRows {
				return err
			}

			if withCommits && cfg != nil {
				if repo, oerr := vcs.Open(cfg); oerr == nil {
					commits, _ := repo.Log(ctx, 200)
					for _, cm := range commits {
						entries = append(entries, logEntry{
							Time:    cm.Date,
							Action:  "commit",
							Subject: cm.Subject,
							Source:  "git",
							Payload: map[string]any{"sha": cm.SHA},
						})
					}
				}
				sort.Slice(entries, func(i, j int) bool {
					return entries[i].Time.After(entries[j].Time)
				})
			}

			if asJSON {
				out := make([]map[string]any, 0, len(entries))
				for _, e := range entries {
					out = append(out, map[string]any{
						"ts":      e.Time.Format(time.RFC3339),
						"action":  e.Action,
						"subject": e.Subject,
						"source":  e.Source,
						"payload": e.Payload,
					})
				}
				return jsonEncode(c.OutOrStdout(), out)
			}

			if len(entries) == 0 {
				fmt.Fprintln(c.OutOrStdout(), "no actions logged")
				return nil
			}
			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "TIME\tACTION\tSUBJECT")
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\n",
					e.Time.Format("2006-01-02 15:04:05"), e.Action, e.Subject)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&sinceFlag, "since", "", "filter by date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max entries")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit as JSON")
	cmd.Flags().BoolVar(&withCommits, "with-commits", false, "interleave backup-repo commits")
	return cmd
}

func subjectFrom(pl map[string]any) string {
	if pl == nil {
		return ""
	}
	if v, ok := pl["display_path"].(string); ok && v != "" {
		return v
	}
	if v, ok := pl["path"].(string); ok && v != "" {
		return v
	}
	if v, ok := pl["dest"].(string); ok && v != "" {
		return v
	}
	if v, ok := pl["id"].(string); ok && v != "" {
		return v
	}
	return ""
}
