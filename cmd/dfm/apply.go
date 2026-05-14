package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/apply"
	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/diffrender"
	"github.com/spf13/cobra"
)

// newApplyCmd builds the `dfm apply` command, which applies a
// previously generated AI suggestion to its target file after a
// confirmation prompt. The --yes flag skips the prompt and --json emits
// a structured result.
func newApplyCmd() *cobra.Command {
	var (
		yes    bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "apply <suggestion-id>",
		Short: "Apply a previously generated suggestion",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			id := args[0]

			s, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			repo := apply.NewRepo(s)
			sg, err := repo.Get(ctx, id)
			if err != nil {
				if errors.Is(err, apply.ErrNotFound) {
					return exitf(exitNotFound, "suggestion not found: %s", id)
				}
				return err
			}
			if sg.Status != apply.StatusPending {
				return exitf(exitAlreadyOrMiss,
					"suggestion %s already decided (status=%s)", id, sg.Status)
			}

			file, err := repo.ResolveFile(ctx, id)
			if err != nil {
				if errors.Is(err, apply.ErrFileMissing) {
					return exitf(exitAlreadyOrMiss,
						"suggestion %s references a file that is no longer tracked", id)
				}
				return err
			}

			out := c.OutOrStdout()
			if !asJSON {
				if goal := parseGoal(sg.Prompt); goal != "" {
					fmt.Fprintf(out, "%s: %s\n\n", file.DisplayPath, goal)
				} else {
					fmt.Fprintf(out, "%s\n\n", file.DisplayPath)
				}
				diffrender.WriteColored(out, sg.Diff)
				fmt.Fprintln(out)
			}

			if !yes {
				if !isTTY() {
					return exitf(exitDrift,
						"refusing to prompt: stdin is not a TTY; pass --yes to confirm")
				}
				ok, err := confirmYN(out, "apply this change? [y/N] ")
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(out, "aborted")
					return nil
				}
			}

			mgr, err := newSnapshotManager(ctx, s)
			if err != nil {
				return err
			}

			start := time.Now()
			res, err := repo.Apply(ctx, mgr, id)
			dur := time.Since(start).Milliseconds()

			baseFields := map[string]any{
				"suggestion_id": id,
				"file_id":       file.ID,
				"display_path":  file.DisplayPath,
				"duration_ms":   dur,
			}

			if err != nil {
				code := classifyApplyExit(err)
				logApplyFailure(ctx, baseFields, err, code)
				return exitf(code, "%v", err)
			}

			baseFields["snapshot_id"] = res.SnapshotID
			baseFields["hunks"] = res.HunksApplied
			baseFields["new_hash"] = res.NewHash
			baseFields["exit_code"] = 0
			audit.Log(ctx, "apply", baseFields)

			if asJSON {
				return writeJSON(out, res)
			}
			short := res.NewHash
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(out, "applied %s to %s\n", id, file.DisplayPath)
			fmt.Fprintf(out, "hunks: %d\n", res.HunksApplied)
			fmt.Fprintf(out, "pre-apply snapshot: %s   (recover with: dfm restore %s)\n",
				res.SnapshotID, res.SnapshotID)
			fmt.Fprintf(out, "new sha256: %s\n", short)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit ApplyResult as JSON")
	return cmd
}

// logApplyFailure emits the apply_failed audit row, branching on
// PostSnapshotError to attach snapshot_id and unwrap the inner error
// for classification. base must contain the canonical apply fields
// (suggestion_id, file_id, display_path, duration_ms). The helper
// merges error_class + exit_code (and optionally snapshot_id) over
// a fresh copy without mutating base.
func logApplyFailure(ctx context.Context, base map[string]any, err error, code int) {
	fields := map[string]any{}
	maps.Copy(fields, base)
	classifyErr := err
	var pse *apply.PostSnapshotError
	if errors.As(err, &pse) {
		fields["snapshot_id"] = pse.SnapshotID
		classifyErr = pse.Err
	}
	fields["error_class"] = classifyApplyError(classifyErr)
	fields["exit_code"] = code
	audit.Log(ctx, "apply_failed", fields)
}

func parseGoal(prompt string) string {
	for line := range strings.SplitSeq(prompt, "\n") {
		if rest, ok := strings.CutPrefix(line, "goal:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func confirmYN(w io.Writer, prompt string) (bool, error) {
	fmt.Fprint(w, prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}

func classifyApplyExit(err error) int {
	switch {
	case errors.Is(err, apply.ErrNotFound):
		return exitNotFound
	case errors.Is(err, apply.ErrAlreadyDecided),
		errors.Is(err, apply.ErrFileMissing):
		return exitAlreadyOrMiss
	case errors.Is(err, apply.ErrDiffEmpty),
		errors.Is(err, apply.ErrDiffMalformed),
		errors.Is(err, apply.ErrDiffDoesNotApply):
		return exitResolveErr
	}
	return 1
}

func classifyApplyError(err error) string {
	switch {
	case errors.Is(err, apply.ErrDiffEmpty):
		return "diff_empty"
	case errors.Is(err, apply.ErrDiffMalformed):
		return "diff_malformed"
	case errors.Is(err, apply.ErrDiffDoesNotApply):
		return "diff_does_not_apply"
	case errors.Is(err, apply.ErrFileMissing):
		return "file_missing"
	}
	return "other"
}

