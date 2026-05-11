package main

import (
	"fmt"
	"os"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/spf13/cobra"
)

var version = "0.0.1-dev"

var (
	flagConfigPath string
	flagVerbose    bool
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dotfiles",
		Short: "Manage, version, and AI-improve your dotfiles",
		Long: "dotfiles tracks your configuration files in a private git repository, " +
			"logs every change, and integrates with AI coding agents (default: Claude Code) " +
			"to suggest improvements as reviewable patches.",
		SilenceUsage: true,
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			path := flagConfigPath
			if path == "" {
				p, err := config.DefaultPath()
				if err != nil {
					return err
				}
				path = p
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			c.SetContext(config.WithContext(c.Context(), cfg))
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&flagConfigPath, "config", "", "path to config.toml")
	cmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output")

	cmd.AddCommand(
		newVersionCmd(),
		newConfigCmd(),
		newMigrateCmd(),
		stubCmd("init", "First-run setup: create config, init/clone private repo, set up log"),
		stubCmd("track", "Begin managing a file"),
		stubCmd("untrack", "Stop managing a file"),
		stubCmd("list", "List tracked files"),
		stubCmd("status", "Show local vs backed-up diff"),
		stubCmd("sync", "Commit and push all changes to the private repo"),
		stubCmd("log", "Show change history"),
		stubCmd("ask", "Ask a free-form question about your dotfiles"),
		stubCmd("suggest", "Have the AI propose improvements as a diff"),
		stubCmd("apply", "Apply a previously generated suggestion"),
	)

	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("dotfiles %s\n", version)
		},
	}
}

func stubCmd(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("%s: not yet implemented\n", name)
		},
	}
}
