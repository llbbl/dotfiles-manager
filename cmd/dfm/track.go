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

// trackOneOptions configures a single invocation of runTrackOne.
// Mirrors the flag set of the cobra `dfm track` command so callers
// outside cobra (e.g. the init wizard's chapter-5 inline track) can
// reuse the same code path without parsing argv.
type trackOneOptions struct {
	Force           bool
	Reset           bool
	DisplayOverride string
}

// runTrackOne is the shared body of `dfm track <path>`. Both the
// cobra RunE wrapper and the init wizard call this helper so the
// resolve + secrets-scan + state-write + snapshot sequence lives in
// exactly one place.
//
// Return contract:
//   - exitCode == 0 and err == nil: success, message printed to stdout.
//   - exitCode != 0 and err == nil: recoverable user-facing failure
//     (resolve error, secrets refusal, already-tracked, suspicious
//     suffix). The relevant message is already on stderr and the cobra
//     wrapper translates the code via os.Exit. Init treats any non-zero
//     code as a soft failure and continues.
//   - err != nil: unrecoverable internal failure (store open, snapshot
//     manager fatal). Propagated unchanged.
//
// Stdout/stderr flow through the passed *cobra.Command so callers can
// redirect output for tests.
func runTrackOne(c *cobra.Command, rawPath string, opts trackOneOptions) (int, error) {
	canonical, display, err := tracker.Resolve(rawPath)
	if err != nil {
		fmt.Fprintln(c.ErrOrStderr(), err)
		return exitResolveErr, nil
	}

	if suffix, ok := tracker.HasBinarySuffix(canonical); ok && !opts.Force {
		fmt.Fprintf(c.ErrOrStderr(),
			"refusing %s: suspicious suffix %q (use --force to override)\n", display, suffix)
		return exitResolveErr, nil
	}

	if opts.DisplayOverride != "" {
		display = opts.DisplayOverride
	}

	s, err := openStore(c.Context())
	if err != nil {
		return 0, err
	}
	defer s.Close()

	mgr, mgrErr := newSnapshotManager(c.Context(), s)
	if mgrErr != nil {
		fmt.Fprintf(c.ErrOrStderr(), "warning: snapshot manager unavailable: %v\n", mgrErr)
	}

	var snapErr error
	f, err := tracker.Track(c.Context(), s, canonical, display, tracker.TrackOptions{
		SkipSecretCheck: opts.Force,
		Reset:           opts.Reset,
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
			return exitSecretsErr, nil
		}
		if errors.Is(err, tracker.ErrAlreadyTracked) {
			fmt.Fprintf(c.ErrOrStderr(),
				"already tracked: %s (use --reset to refresh)\n", display)
			return exitAlreadyOrMiss, nil
		}
		return 0, err
	}

	short := f.LastHash
	if len(short) > 8 {
		short = short[:8]
	}
	fmt.Fprintf(c.OutOrStdout(), "tracked %s (sha256: %s)\n", f.DisplayPath, short)
	return 0, nil
}

// newTrackCmd builds the `dfm track` command, which begins managing
// a file: running the secrets pre-flight scan, recording the file in
// the state DB, and taking an initial snapshot. --force bypasses the
// secrets scan, --reset re-tracks a previously untracked file, and
// --display overrides the human-readable display path.
func newTrackCmd() *cobra.Command {
	var (
		force       bool
		reset       bool
		displayFlag string
	)
	cmd := &cobra.Command{
		Use:   "track <path>",
		Short: "Begin managing a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			code, err := runTrackOne(c, args[0], trackOneOptions{
				Force:           force,
				Reset:           reset,
				DisplayOverride: displayFlag,
			})
			if err != nil {
				return err
			}
			if code != 0 {
				os.Exit(code)
			}
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
