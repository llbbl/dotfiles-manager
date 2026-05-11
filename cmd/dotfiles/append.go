package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/spf13/cobra"
)

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
			f := file
			snap, err := mgr.Snapshot(c.Context(), canonical, &f, snapshot.ReasonPreEdit)
			if err != nil {
				return fmt.Errorf("pre-edit snapshot: %w", err)
			}

			if err := atomicWrite(canonical, newContent, info.Mode().Perm()); err != nil {
				return fmt.Errorf("write %s: %w", canonical, err)
			}

			sum := sha256.Sum256(newContent)
			newHash := hex.EncodeToString(sum[:])

			if _, err := s.DB().ExecContext(c.Context(),
				`UPDATE tracked_files SET last_hash = ? WHERE id = ?`, newHash, file.ID); err != nil {
				return fmt.Errorf("update tracked_files: %w", err)
			}

			audit.Log(c.Context(), "append", map[string]any{
				"display_path":  file.DisplayPath,
				"file_id":       file.ID,
				"snapshot_id":   snap.ID,
				"bytes_appended": len(appendBytes),
				"old_hash":      file.LastHash,
				"new_hash":      newHash,
			})

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

// atomicWrite writes data to a temp file in the same dir then renames into
// place, preserving the given mode. Avoids partial writes on crash.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
