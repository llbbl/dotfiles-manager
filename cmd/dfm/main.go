// Package main is the entry point for the `dfm` CLI, which tracks,
// versions, and AI-improves a user's dotfiles. It manages a local libSQL
// state database and mirrors change history to a private backup git
// repository so configuration changes are reviewable and recoverable.
package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/spf13/cobra"
)

// envLogLevel is the user-facing env var for adjusting the dlog level.
// Matches dlog.EnvLevel; declared here so cmd/dfm doesn't have to import
// the constant just to format error messages about it.
const envLogLevel = "DFM_LOG_LEVEL"

// resolveLogLevel applies the DFM_LOG_LEVEL precedence chain:
//
//  1. --verbose flag (forces debug)
//  2. DFM_LOG_LEVEL env var
//  3. default: error
//
// Returns the resolved level string (one of debug/info/warn/error) or
// an exitError(exitResolveErr) if the env var holds an unrecognized
// value.
func resolveLogLevel(verbose bool, env string) (string, error) {
	if verbose {
		return "debug", nil
	}
	raw := strings.ToLower(strings.TrimSpace(env))
	if raw == "" {
		return "error", nil
	}
	switch raw {
	case "debug", "info", "warn", "error":
		return raw, nil
	}
	return "", exitf(exitResolveErr,
		"invalid %s=%q (must be one of: debug, info, warn, error)",
		envLogLevel, env)
}


// Populated at build time via -ldflags (see .goreleaser.yaml).
var (
	version = "0.0.1-dev"
	commit  = "none"
	date    = "unknown"
)

var (
	flagConfigPath string
	flagVerbose    bool
	flagNoDotenv   bool
	flagDotenvPath string
)

// auditState holds the per-invocation audit logger + store so PersistentPostRunE can close them.
type auditState struct {
	logger *audit.Logger
	store  *store.Store
}

var activeAudit auditState

// dlogState holds the per-invocation debug logger backend closer.
type dlogState struct {
	logger *slog.Logger
	closer io.Closer
}

var activeDlog dlogState

// preMigrationVersion captures the goose schema version recorded in
// the state DB BEFORE PersistentPreRunE's store.New runs pending
// migrations. The `migrate up` summary inspects this to distinguish a
// truly idempotent re-run (preMigrationVersion == post-version) from a
// fresh-DB initial migration that PreRunE silently applied.
//
// Sentinel values:
//   -1: not captured (e.g. config wasn't loaded or peek failed)
//    0: fresh DB, no goose_db_version rows yet
//   >0: that schema version was already recorded
var preMigrationVersion int64 = -1

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}

// newRootCmd builds the top-level `dfm` cobra command. It wires the
// persistent --config/--verbose flags, loads config + dlog + audit logger
// in PersistentPreRunE, closes them in PersistentPostRunE, and registers
// every subcommand.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dfm",
		Short: "Manage, version, and AI-improve your dotfiles",
		Long: "dfm tracks your configuration files in a private git repository, " +
			"logs every change, and integrates with AI coding agents (default: Claude Code) " +
			"to suggest improvements as reviewable patches.",
		SilenceUsage: true,
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			// Resolve the config path early — both the dotenv resolver
			// (for [runtime].dotenv) and the main config Load below
			// consume it.
			path := flagConfigPath
			if path == "" {
				p, err := config.DefaultPath()
				if err != nil {
					return err
				}
				path = p
			}

			// Apply .env BEFORE any env-driven config reads
			// (resolveLogLevel below, plus TURSO_* in config.Load).
			if _, err := resolveAndLoadDotenv(flagNoDotenv, flagDotenvPath, os.Getenv(envFileEnvVar), path); err != nil {
				return err
			}

			level, err := resolveLogLevel(flagVerbose, os.Getenv(envLogLevel))
			if err != nil {
				return err
			}
			dl, dlcloser, dlerr := dlog.New(level)
			if dlerr != nil {
				fmt.Fprintf(os.Stderr, "dlog: %v\n", dlerr)
				dl = dlog.Discard
				dlcloser = io.NopCloser(nil)
			}
			activeDlog = dlogState{logger: dl, closer: dlcloser}
			c.SetContext(dlog.Into(c.Context(), dl))

			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			dl.Debug("config loaded", "path", path, "backend", cfg.Log.Backend)
			c.SetContext(config.WithContext(c.Context(), cfg))

			// Peek at the pre-migration schema version before
			// store.New auto-applies any pending migrations. Failure
			// here is non-fatal: leave the sentinel -1 so consumers
			// can fall back to existing messaging.
			if v, perr := store.CurrentDBVersionBefore(c.Context(), cfg); perr == nil {
				preMigrationVersion = v
			} else {
				preMigrationVersion = -1
			}

			// Best-effort: build an audit Logger backed by libSQL. If the
			// store can't open (e.g. `dfm config` before init), keep
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
			if activeDlog.closer != nil {
				_ = activeDlog.closer.Close()
			}
			activeDlog = dlogState{}
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&flagConfigPath, "config", "", "path to config.toml")
	cmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false,
		"verbose output (synonym for DFM_LOG_LEVEL=debug; takes precedence over the env var)")
	cmd.PersistentFlags().BoolVar(&flagNoDotenv, "no-dotenv", false,
		"skip .env file loading entirely (overrides --dotenv, DFM_ENV_FILE, and [runtime].dotenv)")
	cmd.PersistentFlags().StringVar(&flagDotenvPath, "dotenv", "",
		"explicit path to a .env file to load before reading config (missing file = error)")

	cmd.AddCommand(
		newVersionCmd(),
		newConfigCmd(),
		newMigrateCmd(),
		newScanCmd(),
		newInitCmd(),
		newTrackCmd(),
		newUntrackCmd(),
		newEditCmd(),
		newAppendCmd(),
		newAliasCmd(),
		newListCmd(),
		newStatusCmd(),
		newBackupCmd(),
		newBackupsCmd(),
		newRestoreCmd(),
		newPruneCmd(),
		newSyncCmd(),
		newLogCmd(),
		newAskCmd(),
		newSuggestCmd(),
		newSuggestionsCmd(),
		newApplyCmd(),
		newRejectCmd(),
	)

	return cmd
}

// newVersionCmd builds the `dfm version` command, which prints the
// compiled-in version string and exits.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("dfm %s (commit %s, built %s)\n", version, commit, date)
		},
	}
}

