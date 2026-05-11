package main

import (
	"fmt"
	"os"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/spf13/cobra"
)

var version = "0.0.1-dev"

var (
	flagConfigPath string
	flagVerbose    bool
)

// auditState holds the per-invocation audit logger + store so PersistentPostRunE can close them.
type auditState struct {
	logger *audit.Logger
	store  *store.Store
}

var activeAudit auditState

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

			// Best-effort: build an audit Logger backed by libSQL. If the
			// store can't open (e.g. `dotfiles config` before init), keep
			// the default unset and let callers no-op.
			s, serr := store.New(c.Context(), cfg)
			if serr != nil {
				return nil
			}
			l, lerr := audit.New(c.Context(), cfg, s)
			if lerr != nil {
				_ = s.Close()
				return nil
			}
			activeAudit = auditState{logger: l, store: s}
			audit.SetDefault(l)
			return nil
		},
		PersistentPostRunE: func(c *cobra.Command, _ []string) error {
			if activeAudit.logger != nil {
				_ = activeAudit.logger.Close()
			}
			if activeAudit.store != nil {
				_ = activeAudit.store.Close()
			}
			audit.SetDefault(nil)
			activeAudit = auditState{}
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&flagConfigPath, "config", "", "path to config.toml")
	cmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output")

	cmd.AddCommand(
		newVersionCmd(),
		newConfigCmd(),
		newMigrateCmd(),
		newScanCmd(),
		newInitCmd(),
		newTrackCmd(),
		newUntrackCmd(),
		newListCmd(),
		newStatusCmd(),
		newBackupCmd(),
		newBackupsCmd(),
		newRestoreCmd(),
		newPruneCmd(),
		newSyncCmd(),
		newLogCmd(),
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
