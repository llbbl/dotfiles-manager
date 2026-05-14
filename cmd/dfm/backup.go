package main

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// newBackupCmd builds the `dfm backup` command, which takes a
// manual content snapshot of the given path and records it in the
// snapshot store. --reason tags the snapshot (manual|pre-apply|pre-sync)
// and --json emits the resulting snapshot record as JSON.
func newBackupCmd() *cobra.Command {
	var (
		reasonFlag string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "backup <path>",
		Short: "Take a manual snapshot of a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			canonical, display, err := tracker.Resolve(args[0])
			if err != nil {
				fmt.Fprintln(c.ErrOrStderr(), err)
				os.Exit(exitResolveErr)
			}

			reason := snapshot.Reason(reasonFlag)
			switch reason {
			case snapshot.ReasonManual, snapshot.ReasonPreApply, snapshot.ReasonPreSync:
			default:
				fmt.Fprintf(c.ErrOrStderr(), "invalid --reason %q (manual|pre-apply|pre-sync)\n", reasonFlag)
				os.Exit(exitResolveErr)
			}

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			mgr, err := newSnapshotManager(c.Context(), s)
			if err != nil {
				return err
			}

			snap, err := mgr.Snapshot(c.Context(), canonical, nil, reason)
			if err != nil {
				return err
			}

			if asJSON {
				return writeJSON(c.OutOrStdout(), snapshotJSON(snap))
			}
			short := snap.Hash
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(c.OutOrStdout(), "snapshot %s (sha256: %s, %d bytes) %s\n",
				snap.ID, short, snap.Size, display)
			return nil
		},
	}
	cmd.Flags().StringVar(&reasonFlag, "reason", string(snapshot.ReasonManual), "Reason: manual|pre-apply|pre-sync")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit as JSON")
	return cmd
}

// newBackupsCmd builds the `dfm backups` command, which lists
// snapshots from the snapshot store. An optional path argument scopes
// the listing to one file; --json emits the list as JSON.
func newBackupsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "backups [path]",
		Short: "List snapshots",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			mgr, err := newSnapshotManager(c.Context(), s)
			if err != nil {
				return err
			}

			snaps, err := mgr.List(c.Context(), path)
			if err != nil {
				return err
			}

			if asJSON {
				out := make([]map[string]any, 0, len(snaps))
				for _, sn := range snaps {
					out = append(out, snapshotJSON(sn))
				}
				return writeJSON(c.OutOrStdout(), out)
			}

			if len(snaps) == 0 {
				fmt.Fprintln(c.OutOrStdout(), "no snapshots")
				return nil
			}

			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCREATED\tREASON\tSIZE\tFILE")
			for _, sn := range snaps {
				id := sn.ID
				if len(id) > 10 {
					id = id[:10]
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
					id, sn.CreatedAt.Format("2006-01-02 15:04:05"),
					string(sn.Reason), sn.Size, sn.Path)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit as JSON")
	return cmd
}

// newRestoreCmd builds the `dfm restore` command, which writes a
// snapshot's content back to disk. --to overrides the destination,
// --overwrite allows clobbering an existing file, and --json emits the
// restore result as JSON.
func newRestoreCmd() *cobra.Command {
	var (
		to        string
		overwrite bool
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "restore <id>",
		Short: "Restore a file from a snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			mgr, err := newSnapshotManager(c.Context(), s)
			if err != nil {
				return err
			}

			dest, n, err := mgr.Restore(c.Context(), args[0], to, snapshot.RestoreOptions{Overwrite: overwrite})
			if err != nil {
				if errors.Is(err, snapshot.ErrDestExists) {
					fmt.Fprintf(c.ErrOrStderr(), "destination exists (use --overwrite): %s\n", to)
					os.Exit(exitAlreadyOrMiss)
				}
				if errors.Is(err, snapshot.ErrSnapshotNotFound) {
					fmt.Fprintf(c.ErrOrStderr(), "snapshot not found: %s\n", args[0])
					os.Exit(exitAlreadyOrMiss)
				}
				return err
			}
			if asJSON {
				return writeJSON(c.OutOrStdout(), map[string]any{
					"id":   args[0],
					"dest": dest,
					"size": n,
				})
			}
			fmt.Fprintf(c.OutOrStdout(), "restored %s -> %s (%d bytes)\n", args[0], dest, n)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "destination path (defaults to original)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing destination")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit as JSON")
	return cmd
}

// newPruneCmd builds the `dfm prune` command, which evicts old
// snapshots according to the configured retention policy and size cap.
// --dry-run reports what would be removed without changing anything;
// --json emits the result as JSON.
func newPruneCmd() *cobra.Command {
	var (
		dryRun  bool
		asJSON  bool
		orphans bool
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Evict old snapshots per retention + size cap (or orphan blobs with --orphans)",
		Long: "Without flags, prune evicts old snapshots according to the configured " +
			"retention policy and size cap. With --orphans, prune ignores age/size and " +
			"instead removes on-disk blobs whose SHA is no longer referenced by any " +
			"snapshot row in the state DB.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			mgr, err := newSnapshotManager(c.Context(), s)
			if err != nil {
				return err
			}

			if orphans {
				return runPruneOrphans(c, mgr, dryRun, asJSON, yes)
			}

			var removed int
			var freed int64
			if dryRun {
				removed, freed, err = mgr.PruneDryRun(c.Context())
			} else {
				removed, freed, err = mgr.Prune(c.Context())
			}
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(c.OutOrStdout(), map[string]any{
					"removed":     removed,
					"bytes_freed": freed,
					"dry_run":     dryRun,
				})
			}
			prefix := "pruned"
			if dryRun {
				prefix = "would prune"
			}
			fmt.Fprintf(c.OutOrStdout(), "%s %d snapshots, freed %s\n",
				prefix, removed, humanBytes(freed))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be pruned")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit as JSON")
	cmd.Flags().BoolVar(&orphans, "orphans", false,
		"remove on-disk blobs not referenced by any snapshot row (ignores age/size policy)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (orphan mode)")
	return cmd
}

// runPruneOrphans implements `dfm prune --orphans`. It inventories
// referenced blob hashes, walks the blob root, deletes orphans (or
// reports them in dry-run mode), and emits a text or JSON summary.
func runPruneOrphans(c *cobra.Command, mgr *snapshot.Manager, dryRun, asJSON, yes bool) error {
	ctx := c.Context()
	refs, err := mgr.ReferencedHashes(ctx)
	if err != nil {
		return err
	}
	paths, bytes, err := snapshot.FindOrphans(mgr.Dir(), refs)
	if err != nil {
		return err
	}

	if asJSON {
		out := map[string]any{
			"orphans":     len(paths),
			"bytes_freed": bytes,
			"dry_run":     dryRun,
		}
		if dryRun {
			out["paths"] = paths
		}
		if !dryRun && len(paths) > 0 && !yes {
			// JSON callers cannot answer a prompt; require --yes
			// for a non-dry-run JSON invocation.
			return exitf(exitResolveErr,
				"refusing to delete %d orphan blob(s) without --yes in --json mode", len(paths))
		}
		if !dryRun && len(paths) > 0 {
			if err := snapshot.RemoveOrphans(mgr.Dir(), paths); err != nil {
				return err
			}
			audit.Log(ctx, "snapshot.orphans_pruned", map[string]any{
				"removed":     len(paths),
				"bytes_freed": bytes,
			})
			dlog.From(ctx).Info("orphans pruned", "removed", len(paths), "bytes_freed", bytes)
		}
		return writeJSON(c.OutOrStdout(), out)
	}

	out := c.OutOrStdout()
	if len(paths) == 0 {
		fmt.Fprintln(out, "no orphan blobs")
		return nil
	}

	prefix := "would prune"
	if !dryRun {
		prefix = "pruned"
	}
	if dryRun {
		fmt.Fprintf(out, "%s %d orphan blob(s), would free %s\n",
			prefix, len(paths), humanBytes(bytes))
		for _, p := range paths {
			fmt.Fprintln(out, "  ", p)
		}
		return nil
	}

	if !yes {
		ok, perr := confirmYN(out,
			fmt.Sprintf("delete %d orphan blob(s) totaling %s? [y/N] ",
				len(paths), humanBytes(bytes)))
		if perr != nil {
			return perr
		}
		if !ok {
			fmt.Fprintln(out, "aborted")
			return nil
		}
	}

	if err := snapshot.RemoveOrphans(mgr.Dir(), paths); err != nil {
		return err
	}
	audit.Log(ctx, "snapshot.orphans_pruned", map[string]any{
		"removed":     len(paths),
		"bytes_freed": bytes,
	})
	dlog.From(ctx).Info("orphans pruned", "removed", len(paths), "bytes_freed", bytes)
	fmt.Fprintf(out, "%s %d orphan blob(s), freed %s\n", prefix, len(paths), humanBytes(bytes))
	return nil
}

func snapshotJSON(sn snapshot.Snapshot) map[string]any {
	m := map[string]any{
		"id":           sn.ID,
		"path":         sn.Path,
		"hash":         sn.Hash,
		"size":         sn.Size,
		"reason":       string(sn.Reason),
		"created_at":   sn.CreatedAt.Format(time.RFC3339Nano),
		"storage_path": sn.StoragePath,
	}
	if sn.FileID != nil {
		m["file_id"] = *sn.FileID
	}
	return m
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
