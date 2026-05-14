package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// newEditCmd builds the `dfm edit` command, which takes a pre-edit
// snapshot of a tracked file, opens it in $EDITOR, and on a real change
// updates the stored hash and records an `edit` audit event.
func newEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <file>",
		Short: "Snapshot then open a tracked file in $EDITOR",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			file, canonical, err := resolveTracked(c.Context(), s, args[0])
			if err != nil {
				fmt.Fprintln(c.ErrOrStderr(), err)
				os.Exit(exitAlreadyOrMiss)
			}

			mgr, mgrErr := newSnapshotManager(c.Context(), s)
			if mgrErr != nil {
				return fmt.Errorf("snapshot manager: %w", mgrErr)
			}
			snap, err := snapshot.TakePreEdit(c.Context(), mgr, canonical, file)
			if err != nil {
				return err
			}

			if err := runEditor(canonical); err != nil {
				fmt.Fprintf(c.ErrOrStderr(), "editor exited with error: %v\n", err)
				return exitf(1, "editor failed")
			}

			newHash, err := tracker.HashFile(canonical)
			if err != nil {
				return fmt.Errorf("rehash %s: %w", canonical, err)
			}
			if newHash == file.LastHash {
				fmt.Fprintln(c.OutOrStdout(), "no changes")
				return nil
			}

			if err := tracker.RecordHashChange(c.Context(), s, file, newHash, snap.ID, "edit", nil); err != nil {
				return err
			}

			short := newHash
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(c.OutOrStdout(), "edited %s (sha256: %s)\n", file.DisplayPath, short)
			return nil
		},
	}
}

// resolveTracked finds a tracked file matching the user-supplied target.
// Returns the row plus its canonical filesystem path. Used by edit/append
// which both require the file to already be tracked.
func resolveTracked(ctx context.Context, s *store.Store, target string) (tracker.File, string, error) {
	// Try resolving first (file must exist on disk).
	if canonical, _, err := tracker.Resolve(target); err == nil {
		files, lerr := tracker.List(ctx, s)
		if lerr != nil {
			return tracker.File{}, "", lerr
		}
		for _, f := range files {
			if f.Path == canonical {
				return f, canonical, nil
			}
		}
	}
	// Fall back to display-path / literal match.
	files, lerr := tracker.List(ctx, s)
	if lerr != nil {
		return tracker.File{}, "", lerr
	}
	for _, f := range files {
		if f.DisplayPath == target || f.Path == target {
			return f, f.Path, nil
		}
	}
	return tracker.File{}, "", fmt.Errorf("not tracked: %s", target)
}

// runEditor launches $EDITOR (or vi) against path and waits for it to exit.
// Stdin/Stdout/Stderr are wired to the controlling TTY so interactive
// editors take over the terminal cleanly.
func runEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if strings.TrimSpace(editor) == "" {
		editor = "vi"
	}
	// $EDITOR may include flags ("code -w", "emacs -nw"). Split on
	// whitespace; this matches how git, hg, and friends shell out.
	fields := strings.Fields(editor)
	args := append(fields[1:], path)
	cmd := exec.Command(fields[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
