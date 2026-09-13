package main

import (
	"fmt"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/diffrender"
	"github.com/spf13/cobra"
)

// configShowBody encodes cfg for display, redacting credentials unless
// the caller has already cleared the terminal check.
func configShowBody(cfg *config.Config, showSecrets bool) ([]byte, error) {
	if !showSecrets {
		cfg = cfg.Redacted()
	}
	return cfg.EncodeTOML()
}

// newConfigCmd builds the `dfm config` command group, with `show`
// (print the effective config as TOML) and `path` (print the resolved
// config file path) subcommands.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect dotfiles configuration",
	}
	var showSecrets bool
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the effective config as TOML",
		RunE: func(c *cobra.Command, _ []string) error {
			out := c.OutOrStdout()
			if showSecrets && !diffrender.IsTerminal(out) {
				return exitf(exitInitNoTTY, "--show-secrets prints credentials and only writes to a terminal")
			}
			b, err := configShowBody(config.FromContext(c.Context()), showSecrets)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "# dotenv_source = %q\n", dotenvSource)
			_, err = out.Write(b)
			return err
		},
	}
	show.Flags().BoolVar(&showSecrets, "show-secrets", false, "Print credentials in full (terminal only)")

	cmd.AddCommand(
		show,
		&cobra.Command{
			Use:   "path",
			Short: "Print the resolved config path",
			RunE: func(_ *cobra.Command, _ []string) error {
				if flagConfigPath != "" {
					fmt.Println(flagConfigPath)
					return nil
				}
				p, err := config.DefaultPath()
				if err != nil {
					return err
				}
				fmt.Println(p)
				return nil
			},
		},
	)
	return cmd
}
