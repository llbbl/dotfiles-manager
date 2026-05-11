package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/spf13/cobra"
)

const scanExitFinding = 3

func newScanCmd() *cobra.Command {
	var (
		asJSON bool
		quiet  bool
	)
	cmd := &cobra.Command{
		Use:    "scan <path>",
		Short:  "Scan a file for obvious secrets (pre-flight for track)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			res, err := secrets.ScanFile(args[0])
			if err != nil {
				return err
			}

			if asJSON {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(res); err != nil {
					return err
				}
			} else if !quiet {
				if res.Skipped {
					fmt.Fprintf(c.OutOrStdout(), "skipped: %s\n", res.Reason)
				} else if len(res.Findings) == 0 {
					fmt.Fprintln(c.OutOrStdout(), "no findings")
				} else {
					tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
					fmt.Fprintln(tw, "RULE\tLINE\tEXCERPT")
					for _, f := range res.Findings {
						fmt.Fprintf(tw, "%s\t%d\t%s\n", f.Rule, f.Line, f.Excerpt)
					}
					tw.Flush()
				}
			}

			if len(res.Findings) > 0 {
				os.Exit(scanExitFinding)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit findings as JSON")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress table output")
	return cmd
}
