package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/pathblock"
	"github.com/llbbl/dotfiles-manager/internal/pathimport"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/spf13/cobra"
)

// newPathImportCmd builds the command with preview, confirmation and apply modes.
// Non-interactive application requires --yes; --dry-run takes precedence.
func newPathImportCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		dryRun    bool
		yes       bool
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Scan a tracked rc file for static PATH lines and fold them into one dfm-managed prepend entry",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if shellFlag != "" && fileFlag != "" {
				return exitf(exitResolveErr,
					"path import: --shell and --file are mutually exclusive")
			}
			if shellFlag == "fish" {
				return exitf(exitResolveErr,
					"path import: fish import not yet supported")
			}

			target, family, err := resolvePathTarget(c.Context(), shellFlag, fileFlag)
			if err != nil {
				return err
			}

			// Read the rc bytes. Missing file is treated as "empty" so
			// the user sees the sentinel rather than a stat error.
			data, rerr := os.ReadFile(target)
			if rerr != nil {
				if os.IsNotExist(rerr) {
					data = nil
				} else {
					return fmt.Errorf("read %s: %w", target, rerr)
				}
			}

			report := pathimport.Scan(target, data)

			if asJSON {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(pathimport.JSONShape(report)); err != nil {
					return err
				}
			} else {
				var b strings.Builder
				pathimport.RenderHuman(&b, report)
				if _, err := c.OutOrStdout().Write([]byte(b.String())); err != nil {
					return err
				}
			}

			// Dry-run wins over --yes per the surface matrix.
			if dryRun {
				return nil
			}

			// Nothing to import — short-circuit before snapshot/prompt.
			if report.Proposal == nil || len(report.Proposal.Dirs) == 0 {
				if asJSON {
					if err := writePathImportTrailer(c.OutOrStdout(), pathImportApplyResult{
						Applied: false,
						Reason:  "nothing-to-import",
					}); err != nil {
						return err
					}
				}
				return nil
			}

			// Determine whether to apply.
			apply := yes
			if !apply {
				if !isInteractiveStdin(c.InOrStdin()) {
					return exitf(exitResolveErr,
						"path import: non-interactive shell — pass --yes to apply or --dry-run to preview")
				}
				// Prompt goes to STDERR so --json's stdout stays a
				// clean JSON stream (proposal doc + one-line trailer).
				answer, perr := promptApply(c.ErrOrStderr(), c.InOrStdin())
				if perr != nil {
					return fmt.Errorf("read confirmation: %w", perr)
				}
				apply = answer
			}

			if !apply {
				if asJSON {
					if err := writePathImportTrailer(c.OutOrStdout(), pathImportApplyResult{
						Applied: false,
						Reason:  "user-declined",
					}); err != nil {
						return err
					}
				} else {
					fmt.Fprintln(c.OutOrStdout(), "declined — no changes written.")
				}
				return nil
			}

			// Apply path.
			return runPathImportApply(c, target, family, data, report)
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (mutually exclusive with --shell)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the proposal without writing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "apply without prompting (non-interactive)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a JSON report instead of human-readable text")
	return cmd
}

// pathImportApplyResult is the trailing JSON object emitted in --json
// mode after the structured proposal. It mirrors the human-readable
// summary the apply path prints: outcome + identifying ids.
type pathImportApplyResult struct {
	Applied    bool   `json:"applied"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	MarkerID   string `json:"marker_id,omitempty"`
	DirsCount  int    `json:"dirs_count,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// writePathImportTrailer encodes the apply-result JSON object as a
// single line so the dry-run proposal stays a standalone document on
// stdout and the trailer is unambiguously separable by line-based
// consumers.
func writePathImportTrailer(w io.Writer, res pathImportApplyResult) error {
	buf, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if _, err := w.Write(buf); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

// isInteractiveStdin accepts character devices and injected non-file readers.
// Pipes, redirected files and closed descriptors are non-interactive.
func isInteractiveStdin(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// promptApply emits the Apply? [y/N]: prompt and reads a single line
// from r. Returns true on y/yes/Y/YES, false on anything else (including
// empty Enter). The prompt goes to w (typically stdout) so it lands in
// the same stream as the proposal — tests assert against stdout.
func promptApply(w io.Writer, r io.Reader) (bool, error) {
	if _, err := fmt.Fprint(w, "Apply? [y/N]: "); err != nil {
		return false, err
	}
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	line = strings.TrimSpace(line)
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// runPathImportApply performs the mutation: refuses if a corruption-
// shape prepend entry already exists, deletes the IMPORTABLE lines/
// blocks, splices in a new managed prepend block at the position of
// the first deleted importable line, snapshots + atomic-writes +
// records the hash change in the standard alias.add shape.
//
// `current` is the pre-edit file bytes (the caller already read them);
// passing them in avoids a second os.ReadFile and keeps the
// "what we hashed pre-write" semantics aligned with the snapshot
// that's about to be taken.
func runPathImportApply(c *cobra.Command, target, family string, current []byte, report pathimport.Report) error {
	// Corruption-shape refusal: if rc already has a direction=prepend
	// dfm-managed entry, refuse — path add is the correct way to add
	// dirs to an existing managed entry.
	for _, e := range pathblock.FindEntries(current) {
		if e.Marker.Direction == pathblock.DirectionPrepend {
			return exitf(exitResolveErr,
				"path import: rc file already has a dfm-managed prepend entry — use 'dfm path add' to add dirs instead, or 'dfm path remove' first")
		}
	}

	// We can only mutate a tracked file — the audit + snapshot machinery
	// is keyed on tracker.File. Resolve once the user has confirmed.
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
	info, err := os.Stat(canonical)
	if err != nil {
		return fmt.Errorf("stat %s: %w", canonical, err)
	}

	dirs := report.Proposal.Dirs
	newID := pathblock.MarkerID(pathblock.DirectionPrepend, dirs)
	newBlock := pathblock.Render(family, newID, time.Now().UTC(), pathblock.DirectionPrepend, dirs)

	newContent := pathimport.Splice(current, report.Importable, newBlock)

	mgr, mgrErr := newSnapshotManager(c.Context(), s)
	if mgrErr != nil {
		return fmt.Errorf("snapshot manager: %w", mgrErr)
	}

	// Audit fields mirror path.add; WriteTracked adds snapshot_id. lines_removed
	// counts importable entries folded into the new block.
	snap, err := snapshot.WriteTracked(c.Context(), mgr, s, canonical, file, newContent, info.Mode().Perm(), "path.import", map[string]any{
		"marker_id_new": newID,
		"marker_id_old": "",
		"lines_removed": len(report.Importable),
		"dirs_count":    len(dirs),
		"direction":     pathblock.DirectionPrepend,
		"sub_action":    "import",
	})
	if err != nil {
		return err
	}

	asJSON, _ := c.Flags().GetBool("json")
	if asJSON {
		return writePathImportTrailer(c.OutOrStdout(), pathImportApplyResult{
			Applied:    true,
			SnapshotID: snap.ID,
			MarkerID:   newID,
			DirsCount:  len(dirs),
		})
	}

	fmt.Fprintf(c.OutOrStdout(),
		"applied: %d dirs folded into one prepend entry (marker %s)\n",
		len(dirs), newID)
	fmt.Fprintf(c.OutOrStdout(),
		"snapshot: %s  ->  dfm restore --snapshot %s  to revert\n",
		snap.ID, snap.ID)
	return nil
}
