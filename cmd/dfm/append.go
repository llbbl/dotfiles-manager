package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/llbbl/dotfiles-manager/internal/fsx"
	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// newAppendCmd builds the `dfm append` command, which takes a
// pre-edit snapshot of a tracked file and then appends the supplied text
// to it via an atomic write. The --force flag bypasses the secrets
// pre-flight scan on the appended text.
func newAppendCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "append <file> <text>",
		Short: "Snapshot then append text to a tracked file",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			target := args[0]
			text := args[1]

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			file, canonical, err := resolveTracked(c.Context(), s, target)
			if err != nil {
				fmt.Fprintln(c.ErrOrStderr(), err)
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

			appendBytes := []byte(text)
			newContent := append(append([]byte{}, current...), appendBytes...)

			// Scan only the NEW content for secrets so a user can't slip a
			// credential past `track`'s pre-flight by appending it later.
			res, scanErr := secrets.ScanReader(bytes.NewReader(newContent))
			if scanErr != nil {
				return fmt.Errorf("secret scan: %w", scanErr)
			}
			if !res.Skipped && len(res.Findings) > 0 {
				if !force {
					tw := tabwriter.NewWriter(c.ErrOrStderr(), 0, 0, 2, ' ', 0)
					fmt.Fprintln(tw, "RULE\tLINE\tEXCERPT")
					for _, fi := range res.Findings {
						fmt.Fprintf(tw, "%s\t%d\t%s\n", fi.Rule, fi.Line, fi.Excerpt)
					}
					tw.Flush()
					fmt.Fprintln(c.ErrOrStderr(), "append aborted: secrets detected (--force to override)")
					os.Exit(exitSecretsErr)
				}
				fmt.Fprintf(c.ErrOrStderr(), "warning: %d secret finding(s) in appended content; proceeding due to --force\n", len(res.Findings))
			}

			mgr, mgrErr := newSnapshotManager(c.Context(), s)
			if mgrErr != nil {
				return fmt.Errorf("snapshot manager: %w", mgrErr)
			}
			snap, err := snapshot.TakePreEdit(c.Context(), mgr, canonical, file)
			if err != nil {
				return err
			}

			if err := fsx.AtomicWrite(canonical, newContent, info.Mode().Perm()); err != nil {
				return fmt.Errorf("write %s: %w", canonical, err)
			}

			sum := sha256.Sum256(newContent)
			newHash := hex.EncodeToString(sum[:])

			if err := tracker.RecordHashChange(c.Context(), s, file, newHash, snap.ID, "append", map[string]any{
				"bytes_appended": len(appendBytes),
			}); err != nil {
				return err
			}

			short := newHash
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(c.OutOrStdout(), "appended %d byte(s) to %s (sha256: %s)\n",
				len(appendBytes), file.DisplayPath, short)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "append even if secrets are detected")
	return cmd
}

