package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/ai"
	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/diffrender"
	"github.com/llbbl/dotfiles-manager/internal/ids"
	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// exitError is an error carrying a desired process exit code. The root
// cobra command's wrapper converts it to os.Exit at the very top.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func exitf(code int, format string, args ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, args...)}
}

// newSuggestCmd builds the `dotfiles suggest` command, which asks the
// configured AI provider to propose improvements to a tracked file as a
// unified diff, stores the result as a pending suggestion, and prints
// the rendered diff. --goal supplies an optional intent string and
// --json emits the suggestion record as JSON.
func newSuggestCmd() *cobra.Command {
	var (
		goal   string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "suggest <file>",
		Short: "Have the AI propose improvements as a diff",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg := config.FromContext(c.Context())
			if cfg == nil {
				return errors.New("config not loaded")
			}

			canonical, display, err := tracker.Resolve(args[0])
			if err != nil {
				return exitf(exitResolveErr, "%v", err)
			}

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			file, err := lookupTrackedFile(c.Context(), s, canonical, display)
			if err != nil {
				return err
			}

			content, err := readBounded(canonical, secrets.MaxBytes)
			if err != nil {
				return exitf(exitResolveErr, "%v", err)
			}

			prov, err := providerFactory(cfg)
			if err != nil {
				return exitf(exitResolveErr, "%v", err)
			}

			res, err := prov.Suggest(c.Context(), ai.SuggestRequest{
				File:    file,
				Content: content,
				Goal:    goal,
			})
			fields := map[string]any{
				"provider":     prov.Name(),
				"file_id":      file.ID,
				"display_path": file.DisplayPath,
				"duration_ms":  res.Duration.Milliseconds(),
			}
			if err != nil {
				code := classifyExit(err)
				fields["exit_code"] = code
				audit.Log(c.Context(), "suggest", fields)
				return exitf(code, "%v", err)
			}

			id, idErr := ids.New()
			if idErr != nil {
				return fmt.Errorf("generate id: %w", idErr)
			}
			createdAt := time.Now().UTC().Format(time.RFC3339)
			prompt := renderPromptForRecord(file, goal)

			if _, err := s.DB().ExecContext(c.Context(),
				`INSERT INTO suggestions (id, file_id, provider, prompt, diff, status, created_at)
				 VALUES (?, ?, ?, ?, ?, 'pending', ?)`,
				id, file.ID, prov.Name(), prompt, res.Diff, createdAt,
			); err != nil {
				return fmt.Errorf("insert suggestion: %w", err)
			}

			fields["suggestion_id"] = id
			fields["exit_code"] = 0
			audit.Log(c.Context(), "suggest", fields)

			if asJSON {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"id":         id,
					"summary":    res.Summary,
					"diff":       res.Diff,
					"provider":   prov.Name(),
					"created_at": createdAt,
				})
			}

			out := c.OutOrStdout()
			fmt.Fprintln(out, res.Summary)
			fmt.Fprintln(out)
			diffrender.WriteColored(out, res.Diff)
			fmt.Fprintln(out)
			fmt.Fprintf(out, "%s\n# review and apply with: dotfiles apply %s\n", id, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&goal, "goal", "", "what to optimize for; empty means general improvement")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit suggestion as JSON")
	return cmd
}

// lookupTrackedFile loads the tracked_files row for the given canonical
// path, or returns an exitError with exitNotFound when missing.
func lookupTrackedFile(ctx context.Context, s *store.Store, canonical, display string) (tracker.File, error) {
	files, err := tracker.List(ctx, s)
	if err != nil {
		return tracker.File{}, err
	}
	for _, f := range files {
		if f.Path == canonical {
			return f, nil
		}
	}
	return tracker.File{}, exitf(exitNotFound,
		"not tracked: %s (run `dotfiles track %s` first)", display, display)
}

// readBounded reads a file refusing payloads larger than max bytes.
func readBounded(path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(max) {
		return nil, fmt.Errorf("refusing %s: file exceeds %d bytes", path, max)
	}
	buf, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > max {
		return nil, fmt.Errorf("refusing %s: file exceeds %d bytes", path, max)
	}
	return buf, nil
}

// renderPromptForRecord rebuilds the prompt body for persistence in the
// suggestions row. It mirrors claudecode.renderSuggestPrompt; duplicated
// here to avoid leaking that helper through the package boundary.
func renderPromptForRecord(f tracker.File, goal string) string {
	g := strings.TrimSpace(goal)
	if g == "" {
		g = "improve readability, correctness, and conventions"
	}
	var b strings.Builder
	b.WriteString("file: ")
	b.WriteString(f.DisplayPath)
	b.WriteString("\ngoal: ")
	b.WriteString(g)
	return b.String()
}

