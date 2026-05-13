package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

const (
	exitResolveErr    = 2
	exitSecretsErr    = 3
	exitAlreadyOrMiss = 4
)

// newTrackCmd builds the `dfm track` command, which begins managing
// a file: running the secrets pre-flight scan, recording the file in
// the state DB, and taking an initial snapshot. --force bypasses the
// secrets scan, --reset re-tracks a previously untracked file, and
// --display overrides the human-readable display path.
func newTrackCmd() *cobra.Command {
	var (
		force        bool
		reset        bool
		displayFlag  string
	)
	cmd := &cobra.Command{
		Use:   "track <path>",
		Short: "Begin managing a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			canonical, display, err := tracker.Resolve(args[0])
			if err != nil {
				fmt.Fprintln(c.ErrOrStderr(), err)
				os.Exit(exitResolveErr)
			}

			if suffix, ok := tracker.HasBinarySuffix(canonical); ok && !force {
				fmt.Fprintf(c.ErrOrStderr(),
					"refusing %s: suspicious suffix %q (use --force to override)\n", display, suffix)
				os.Exit(exitResolveErr)
			}

			if displayFlag != "" {
				display = displayFlag
			}

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			mgr, mgrErr := newSnapshotManager(c.Context(), s)
			if mgrErr != nil {
				fmt.Fprintf(c.ErrOrStderr(), "warning: snapshot manager unavailable: %v\n", mgrErr)
			}

			var snapErr error
			f, err := tracker.Track(c.Context(), s, canonical, display, tracker.TrackOptions{
				SkipSecretCheck: force,
				Reset:           reset,
				AfterCommit: func(ctx context.Context, file tracker.File) error {
					if mgr == nil {
						return nil
					}
					f := file
					if _, e := mgr.Snapshot(ctx, file.Path, &f, snapshot.ReasonTrack); e != nil {
						snapErr = e
					}
					return nil
				},
			})
			if snapErr != nil {
				fmt.Fprintf(c.ErrOrStderr(), "warning: failed to snapshot %s: %v\n", display, snapErr)
			}
			if err != nil {
				var secErr *tracker.SecretsError
				if errors.As(err, &secErr) {
					tw := tabwriter.NewWriter(c.ErrOrStderr(), 0, 0, 2, ' ', 0)
					fmt.Fprintln(tw, "RULE\tLINE\tEXCERPT")
					for _, fi := range secErr.Result.Findings {
						fmt.Fprintf(tw, "%s\t%d\t%s\n", fi.Rule, fi.Line, fi.Excerpt)
					}
					tw.Flush()
					fmt.Fprintln(c.ErrOrStderr(), "track aborted: secrets detected (--force to override)")
					os.Exit(exitSecretsErr)
				}
				if errors.Is(err, tracker.ErrAlreadyTracked) {
					fmt.Fprintf(c.ErrOrStderr(),
						"already tracked: %s (use --reset to refresh)\n", display)
					os.Exit(exitAlreadyOrMiss)
				}
				return err
			}

			short := f.LastHash
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(c.OutOrStdout(), "tracked %s (sha256: %s)\n", f.DisplayPath, short)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Track even if secrets are detected")
	cmd.Flags().BoolVar(&reset, "reset", false, "Re-track an existing file; refresh hash + added_at")
	cmd.Flags().StringVar(&displayFlag, "display", "", "Override the display path")
	return cmd
}

// openStore returns the store for the current command. It prefers
// the store opened by the root command's PersistentPreRunE (which
// also runs goose migrations), avoiding a redundant second store
// open and second goose invocation per command. Callers must call
// Close() on the returned store; when the cached store is returned
// the Close is a no-op so the root command's PostRunE can clean up.
func openStore(ctx context.Context) (*store.Store, error) {
	if activeAudit.store != nil {
		return activeAudit.store.SharedRef(), nil
	}
	cfg := config.FromContext(ctx)
	if cfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}
	return store.New(ctx, cfg)
}

func newSnapshotManager(ctx context.Context, s *store.Store) (*snapshot.Manager, error) {
	cfg := config.FromContext(ctx)
	if cfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}
	return snapshot.New(s, snapshot.Config{
		Dir:           cfg.Backup.Dir,
		MaxTotalMB:    cfg.Backup.MaxTotalMB,
		RetentionDays: cfg.Backup.RetentionDays,
	})
}
