package main

import (
	"fmt"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect dotfiles configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Print the effective config as TOML",
			RunE: func(c *cobra.Command, _ []string) error {
				cfg := config.FromContext(c.Context())
				b, err := cfg.EncodeTOML()
				if err != nil {
					return err
				}
				fmt.Print(string(b))
				return nil
			},
		},
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
