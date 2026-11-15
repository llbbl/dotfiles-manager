package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/llbbl/dotfiles-manager/internal/pathblock"
	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/llbbl/dotfiles-manager/internal/shellrc"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// newPathCmd builds the `dfm path` command group: a parent with three
// stub subcommands. Real business logic lives in follow-up tasks; the
// stubs exist so the CLI surface is reachable from day one.
func newPathCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Manage PATH entries in tracked rc files (coalesced, idempotent)",
	}
	cmd.AddCommand(newPathAddCmd(), newPathRemoveCmd(), newPathListCmd(), newPathImportCmd(),
		newPathFragmentCmd(), newPathHookCmd(), newPathMigrateCmd())
	return cmd
}

// pathUseFragment reports whether `dfm path migrate` has moved the
// managed entries into the generated fragment. A context with no config
// attached answers false, which is the pre-migrate behaviour.
func pathUseFragment(ctx context.Context) bool {
	cfg := config.FromContext(ctx)
	return cfg != nil && cfg.Path.UseFragment
}

// resolvePathTarget is resolveAliasTarget's sibling for the path
// commands: identical file resolution, three-way family instead of two.
// It is also the only place the use_fragment switch is read, so every
// path subcommand follows a migration without a flag of its own. An
// explicit --file or --shell names a file and always wins.
func resolvePathTarget(ctx context.Context, shellFlag, fileFlag string) (string, string, error) {
	if shellFlag == "" && fileFlag == "" && pathUseFragment(ctx) {
		frag := config.FragmentPath(pathFragmentName(shellrc.PathFamily(shellrc.Detect())))
		return frag, shellrc.PathFamily(shellrc.FromFilename(frag)), nil
	}
	target, shell, err := shellrc.ResolveTarget(shellFlag, fileFlag)
	if err != nil {
		return "", "", err
	}
	return target, shellrc.PathFamily(shell), nil
}

// writeTrackedEdit tracks path if it is not tracked yet, snapshots it,
// writes content, and records the hash change. The file must already
// exist. cmdName only shapes the refusal message, so it names the
// command the user is actually holding rather than dfm track.
func writeTrackedEdit(c *cobra.Command, s *store.Store, cmdName, path string, content []byte,
	action string, meta map[string]any, force bool) (tracker.File, error) {
	ctx := c.Context()
	file, canonical, err := resolveTracked(ctx, s, path)
	if err != nil {
		code, terr := runTrackOne(c, path, trackOneOptions{Force: force})
		if terr != nil {
			return tracker.File{}, terr
		}
		if code != 0 {
			return tracker.File{}, exitf(code,
				"%s: could not track %s. Re-run with --force to proceed anyway", cmdName, path)
		}
		if file, canonical, err = resolveTracked(ctx, s, path); err != nil {
			return tracker.File{}, err
		}
	}

	mgr, err := newSnapshotManager(ctx, s)
	if err != nil {
		return tracker.File{}, fmt.Errorf("snapshot manager: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, serr := os.Stat(canonical); serr == nil {
		mode = info.Mode().Perm()
	}
	if _, err := snapshot.WriteTracked(ctx, mgr, s, canonical, file, content, mode, action, meta); err != nil {
		return tracker.File{}, err
	}
	return file, nil
}

func newPathAddCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		appendDir bool
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "add <dir>",
		Short: "Add a directory to the dfm-managed PATH entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			dir := args[0]
			if dir == "" {
				return exitf(exitResolveErr, "path add: <dir> must not be empty")
			}
			// Strict mutex: alias add tolerates both flags together; the
			// path-cmd spec (§4.1) deliberately diverges.
			if shellFlag != "" && fileFlag != "" {
				return exitf(exitResolveErr,
					"path add: --shell and --file are mutually exclusive")
			}

			target, family, err := resolvePathTarget(c.Context(), shellFlag, fileFlag)
			if err != nil {
				return err
			}

			direction := pathblock.DirectionPrepend
			if appendDir {
				direction = pathblock.DirectionAppend
			}

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			file, canonical, err := resolveTracked(c.Context(), s, target)
			if err != nil {
				fmt.Fprintf(c.ErrOrStderr(),
					"not tracked: %s. Run: dfm track %s\n", target, target)
				os.Exit(exitAlreadyOrMiss)
			}

			current, err := os.ReadFile(canonical)
			if err != nil {
				return fmt.Errorf("read %s: %w", canonical, err)
			}
			info, err := os.Stat(canonical)
			if err != nil {
				return fmt.Errorf("stat %s: %w", canonical, err)
			}

			// Locate every managed entry and partition by direction.
			entries := pathblock.FindEntries(current)
			var matching []pathblock.Entry
			for _, e := range entries {
				if e.Marker.Direction == direction {
					matching = append(matching, e)
				}
			}
			// Corruption guard (§5.4 / §7 case #19): more than one
			// managed entry for the same direction means a manual edit
			// went wrong. Refuse to auto-merge.
			if len(matching) > 1 {
				return exitf(exitResolveErr,
					"multiple dfm-managed %s entries found in %s; please inspect and clean up manually",
					direction, file.DisplayPath)
			}

			var (
				existing     *pathblock.Entry
				existingDirs []string
				markerIDOld  string
			)
			if len(matching) == 1 {
				existing = &matching[0]
				existingDirs = append(existingDirs, existing.Marker.Dirs...)
				markerIDOld = existing.Marker.ID
			}

			// Dedup against existing dirs in this direction using
			// normalized comparison; the user's literal token is what
			// gets persisted.
			needle := pathblock.NormalizeDir(dir)
			for _, d := range existingDirs {
				if pathblock.NormalizeDir(d) == needle {
					return exitf(exitAlreadyOrMiss,
						"%q already managed in %s entry of %s",
						dir, direction, file.DisplayPath)
				}
			}

			newDirs := append([]string{}, existingDirs...)
			newDirs = append(newDirs, dir)

			// Secret scan on the dir token. Mirror alias add's UX: print
			// a tab-separated table on stderr, exit exitSecretsErr
			// unless --force is set. --force does NOT bypass dedup.
			res, scanErr := secrets.ScanReader(bytes.NewReader([]byte(dir)))
			if scanErr != nil {
				return fmt.Errorf("secret scan: %w", scanErr)
			}
			secretFlagged := !res.Skipped && len(res.Findings) > 0
			if secretFlagged {
				if !force {
					tw := tabwriter.NewWriter(c.ErrOrStderr(), 0, 0, 2, ' ', 0)
					fmt.Fprintln(tw, "RULE\tLINE\tEXCERPT")
					for _, fi := range res.Findings {
						fmt.Fprintf(tw, "%s\t%d\t%s\n", fi.Rule, fi.Line, fi.Excerpt)
					}
					tw.Flush()
					return exitf(exitSecretsErr,
						"path add aborted: secrets detected (--force to override)")
				}
				fmt.Fprintf(c.ErrOrStderr(),
					"warning: %d secret finding(s) in dir token; proceeding due to --force\n",
					len(res.Findings))
			}

			// WriteTracked below takes the pre-edit snapshot before writing.
			mgr, mgrErr := newSnapshotManager(c.Context(), s)
			if mgrErr != nil {
				return fmt.Errorf("snapshot manager: %w", mgrErr)
			}

			newID := pathblock.MarkerID(direction, newDirs)
			block := pathblock.Render(family, newID, time.Now().UTC(), direction, newDirs)

			var newContent []byte
			if existing != nil {
				// In-place splice: replace the old block bytes with the
				// new block. BlockEnd already includes the trailing
				// newline if there was one — every renderer always
				// emits a trailing newline, so byte semantics line up.
				newContent = make([]byte, 0, len(current)-(existing.BlockEnd-existing.BlockStart)+len(block))
				newContent = append(newContent, current[:existing.BlockStart]...)
				newContent = append(newContent, block...)
				newContent = append(newContent, current[existing.BlockEnd:]...)
			} else {
				// Append at EOF. Ensure exactly one blank line of
				// breathing room before the block when the file has
				// existing content — readability matches what installer
				// snippets do.
				newContent = make([]byte, 0, len(current)+len(block)+2)
				newContent = append(newContent, current...)
				if len(current) > 0 {
					if !bytes.HasSuffix(current, []byte("\n")) {
						newContent = append(newContent, '\n')
					}
					if !bytes.HasSuffix(newContent, []byte("\n\n")) {
						newContent = append(newContent, '\n')
					}
				}
				newContent = append(newContent, block...)
			}

			subAction := "add"
			if force && secretFlagged {
				subAction = "force-add"
			}

			// Privacy note: alias add deliberately omits the alias body
			// from audit. The dir token is the audited unit here; it's
			// already public-facing (it lives in PATH at runtime), so
			// recording it lets audit consumers reconstruct the
			// transition without re-reading the rc file.
			if _, err := snapshot.WriteTracked(c.Context(), mgr, s, canonical, file, newContent, info.Mode().Perm(), "path.add", map[string]any{
				"dir":           dir,
				"direction":     direction,
				"marker_id_new": newID,
				"marker_id_old": markerIDOld,
				"sub_action":    subAction,
			}); err != nil {
				return err
			}

			// fish_add_path drops a dir that isn't on disk while the POSIX blocks
			// add it anyway. A path still holding a variable dfm can't expand is
			// skipped.
			if filepath.IsAbs(needle) && !strings.Contains(needle, "$") {
				if _, err := os.Stat(needle); errors.Is(err, fs.ErrNotExist) {
					fmt.Fprintf(c.ErrOrStderr(),
						"warning: %s does not exist yet; POSIX shells add it now, fish skips it until it exists\n", dir)
				}
			}

			fmt.Fprintf(c.OutOrStdout(),
				"added %q to %s entry in %s\n", dir, direction, file.DisplayPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (mutually exclusive with --shell)")
	cmd.Flags().BoolVar(&appendDir, "append", false, "write into the append-direction entry (default: prepend)")
	cmd.Flags().BoolVar(&force, "force", false, "bypass secret scan (does NOT bypass dedup)")
	return cmd
}

func newPathRemoveCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
	)
	cmd := &cobra.Command{
		Use:   "remove <dir>",
		Short: "Remove a directory from the dfm-managed PATH entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			dir := args[0]
			if dir == "" {
				return exitf(exitResolveErr, "path remove: <dir> must not be empty")
			}
			if shellFlag != "" && fileFlag != "" {
				return exitf(exitResolveErr,
					"path remove: --shell and --file are mutually exclusive")
			}

			target, family, err := resolvePathTarget(c.Context(), shellFlag, fileFlag)
			if err != nil {
				return err
			}

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			file, canonical, err := resolveTracked(c.Context(), s, target)
			if err != nil {
				fmt.Fprintf(c.ErrOrStderr(),
					"not tracked: %s. Run: dfm track %s\n", target, target)
				os.Exit(exitAlreadyOrMiss)
			}

			current, err := os.ReadFile(canonical)
			if err != nil {
				return fmt.Errorf("read %s: %w", canonical, err)
			}
			info, err := os.Stat(canonical)
			if err != nil {
				return fmt.Errorf("stat %s: %w", canonical, err)
			}

			// Search all managed entries (both directions). First match wins.
			needle := pathblock.NormalizeDir(dir)
			entries := pathblock.FindEntries(current)
			var (
				matchedEntry *pathblock.Entry
				matchedIdx   = -1
			)
			for i := range entries {
				e := &entries[i]
				for j, d := range e.Marker.Dirs {
					if pathblock.NormalizeDir(d) == needle {
						matchedEntry = e
						matchedIdx = j
						break
					}
				}
				if matchedEntry != nil {
					break
				}
			}
			if matchedEntry == nil {
				// Not managed: clean exit, no mutation, no snapshot, no audit.
				return exitf(exitAlreadyOrMiss, "not managed by dfm: %s", dir)
			}

			direction := matchedEntry.Marker.Direction
			markerIDOld := matchedEntry.Marker.ID
			// Splice out the matched dir from the entry's dirs list.
			newDirs := make([]string, 0, len(matchedEntry.Marker.Dirs)-1)
			newDirs = append(newDirs, matchedEntry.Marker.Dirs[:matchedIdx]...)
			newDirs = append(newDirs, matchedEntry.Marker.Dirs[matchedIdx+1:]...)

			// Snapshot before mutation. ReasonPreEdit matches the
			// add-flow convention.
			mgr, mgrErr := newSnapshotManager(c.Context(), s)
			if mgrErr != nil {
				return fmt.Errorf("snapshot manager: %w", mgrErr)
			}

			var (
				newContent   []byte
				markerIDNew  string
				entryDeleted bool
			)
			if len(newDirs) == 0 {
				// Drain entire block. If the byte preceding BlockStart is
				// a newline AND the byte after BlockEnd is also a newline,
				// we'd leave a doubled blank line — trim one to keep the
				// file tidy. We only trim if the file had a separator blank
				// line before the block (mirroring how add adds one).
				entryDeleted = true
				start := matchedEntry.BlockStart
				end := matchedEntry.BlockEnd
				// Trim a single trailing blank line left behind if the
				// block was preceded by "\n\n" and followed by content.
				if start >= 2 && current[start-1] == '\n' && current[start-2] == '\n' && end < len(current) {
					start--
				}
				newContent = make([]byte, 0, len(current)-(end-start))
				newContent = append(newContent, current[:start]...)
				newContent = append(newContent, current[end:]...)
			} else {
				markerIDNew = pathblock.MarkerID(direction, newDirs)
				block := pathblock.Render(family, markerIDNew, time.Now().UTC(), direction, newDirs)
				newContent = make([]byte, 0, len(current)-(matchedEntry.BlockEnd-matchedEntry.BlockStart)+len(block))
				newContent = append(newContent, current[:matchedEntry.BlockStart]...)
				newContent = append(newContent, block...)
				newContent = append(newContent, current[matchedEntry.BlockEnd:]...)
			}

			if _, err := snapshot.WriteTracked(c.Context(), mgr, s, canonical, file, newContent, info.Mode().Perm(), "path.remove", map[string]any{
				"dir":           dir,
				"direction":     direction,
				"marker_id_new": markerIDNew,
				"marker_id_old": markerIDOld,
				"entry_deleted": entryDeleted,
			}); err != nil {
				return err
			}

			if entryDeleted {
				fmt.Fprintf(c.OutOrStdout(),
					"removed %q from %s entry in %s (entry deleted)\n", dir, direction, file.DisplayPath)
			} else {
				fmt.Fprintf(c.OutOrStdout(),
					"removed %q from %s entry in %s\n", dir, direction, file.DisplayPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (mutually exclusive with --shell)")
	return cmd
}

func newPathListCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List dfm-managed PATH entries",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if shellFlag != "" && fileFlag != "" {
				return exitf(exitResolveErr,
					"path list: --shell and --file are mutually exclusive")
			}

			target, _, err := resolvePathTarget(c.Context(), shellFlag, fileFlag)
			if err != nil {
				return err
			}

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			// list is read-only and tolerates an untracked rc file —
			// nothing is mutated and the on-disk markers are the sole
			// source of truth.
			if _, _, rerr := resolveTracked(c.Context(), s, target); rerr != nil {
				dlog.Discard.Debug("path list: target not tracked", "target", target)
			}

			data, err := os.ReadFile(target)
			if err != nil {
				if os.IsNotExist(err) {
					// Treat a missing rc as "no managed entries". This is
					// kinder than erroring — `dfm path list` is the cheapest
					// possible inspection and should never blow up.
					data = nil
				} else {
					return fmt.Errorf("read %s: %w", target, err)
				}
			}

			rows := pathblock.ListRows(pathblock.FindEntries(data))

			if asJSON {
				out := rows
				if out == nil {
					out = []pathblock.Row{}
				}
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(out); err != nil {
					return fmt.Errorf("encode json: %w", err)
				}
				return nil
			}

			if len(rows) == 0 {
				return nil
			}
			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "DIR\tDIRECTION\tMARKER_ID")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Dir, r.Direction, r.MarkerID)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (mutually exclusive with --shell)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of tab-separated rows")
	return cmd
}
