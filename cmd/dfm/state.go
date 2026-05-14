package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/llbbl/dotfiles-manager/internal/stateimport"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/spf13/cobra"
)

// newStateCmd builds the `dfm state` parent command. Today the
// only subcommand is `import`; `export` is a planned follow-up.
func newStateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Manage the state database",
	}
	cmd.AddCommand(newStateImportCmd())
	return cmd
}

// newStateImportCmd builds `dfm state import`, which copies rows
// from a source state DB into the active target state DB.
func newStateImportCmd() *cobra.Command {
	var (
		fromURL    string
		tablesFlag string
		dryRun     bool
		yes        bool
		replace    bool
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import rows from another dfm state DB into this one",
		Long: "Copies tracked_files, snapshots, and (optionally) suggestions / actions " +
			"from --from <url> into the active state DB. Snapshot rows whose blob is " +
			"not present in the local blob store are skipped with a warning. Existing " +
			"rows are skipped unless --replace is set.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if strings.TrimSpace(fromURL) == "" {
				return exitf(exitResolveErr, "--from is required")
			}
			tables, err := stateimport.ValidateTables(stateimport.ParseTablesFlag(tablesFlag))
			if err != nil {
				return exitf(exitResolveErr, "%v", err)
			}

			ctx := c.Context()
			cfg := config.FromContext(ctx)
			if cfg == nil {
				return errors.New("config not loaded")
			}

			// Same-URL guard.
			if sameStateURL(fromURL, cfg.State.URL) {
				return exitf(exitResolveErr,
					"--from %q resolves to the same DB as the active target; refusing", fromURL)
			}

			// Open target (reuse the migrated handle the root cmd
			// already opened).
			target, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer target.Close()

			// Open source without auto-migration. We don't want to
			// mutate the source DB on a read-only-ish import.
			srcDB, srcTarget, err := openSourceDB(ctx, fromURL)
			if err != nil {
				return fmt.Errorf("open source: %w", err)
			}
			defer srcDB.Close()
			dlog.From(ctx).Debug("state import source", "target", srcTarget)

			if !dryRun && !yes {
				out := c.OutOrStdout()
				ok, perr := confirmYN(out,
					fmt.Sprintf("import tables [%s] from %q? [y/N] ",
						strings.Join(tables, ","), srcTarget))
				if perr != nil {
					return perr
				}
				if !ok {
					fmt.Fprintln(out, "aborted")
					return nil
				}
			}

			res, err := stateimport.Import(ctx, stateimport.Options{
				Source:     srcDB,
				Target:     target.DB(),
				Tables:     tables,
				DryRun:     dryRun,
				Replace:    replace,
				BlobExists: stateimport.LocalBlobExistsFunc(cfg.Backup.Dir),
			})
			if err != nil {
				return err
			}

			audit.Log(ctx, "state.imported", map[string]any{
				"source":  srcTarget,
				"tables":  tables,
				"dry_run": dryRun,
				"replace": replace,
			})

			return emitImportResult(c, res, asJSON)
		},
	}
	cmd.Flags().StringVar(&fromURL, "from", "",
		"source state DB URL (file://<path> or libsql://<host>?authToken=...)")
	cmd.Flags().StringVar(&tablesFlag, "tables", "",
		"comma-separated tables to import (default: tracked_files,snapshots; "+
			"allowed: tracked_files,snapshots,suggestions,actions)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be imported, no writes")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&replace, "replace", false,
		"overwrite rows that already exist in the target (default: skip)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit summary as JSON")
	return cmd
}

// sameStateURL returns true if a and b refer to the same state DB
// after light normalization (trim, strip "file://" for local
// paths). Remote URLs are compared by parsed scheme+host+path.
func sameStateURL(a, b string) bool {
	na := normalizeStateURL(a)
	nb := normalizeStateURL(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb
}

func normalizeStateURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(s, "file://"); ok {
		return "file://" + rest
	}
	// For remote URLs strip query string (auth tokens etc) before
	// comparing.
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// openSourceDB opens the source state DB at fromURL without
// running migrations. Returns the *sql.DB, the resolved target
// string (for logging), and any error.
func openSourceDB(ctx context.Context, fromURL string) (*sql.DB, string, error) {
	// store.Open consumes a *config.Config; build a minimal one
	// scoped to the source URL. We pull the auth token off the
	// URL query string for remote sources so users can pass
	// libsql://host?authToken=... directly.
	srcCfg := &config.Config{State: config.StateConfig{URL: fromURL}}
	if tok := extractAuthToken(fromURL); tok != "" {
		srcCfg.State.AuthToken = tok
	}
	return store.Open(ctx, srcCfg)
}

// extractAuthToken pulls authToken from the query string of a URL
// (case-insensitive on the key). Returns "" if absent or
// unparseable.
func extractAuthToken(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, k := range []string{"authToken", "auth_token", "token"} {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func emitImportResult(c *cobra.Command, res stateimport.Result, asJSON bool) error {
	out := c.OutOrStdout()
	imported, skippedExisting, skippedMissingBlob := res.Totals()

	if asJSON {
		tables := make([]map[string]any, 0, len(res.Tables))
		for _, t := range res.Tables {
			tables = append(tables, map[string]any{
				"table":                t.Table,
				"imported":             t.Imported,
				"skipped_existing":     t.SkippedExisting,
				"skipped_missing_blob": t.SkippedMissingBlob,
			})
		}
		return writeJSON(out, map[string]any{
			"dry_run":              res.DryRun,
			"imported":             imported,
			"skipped_existing":     skippedExisting,
			"skipped_missing_blob": skippedMissingBlob,
			"tables":               tables,
			"warnings":             res.Warnings,
		})
	}

	for _, w := range res.Warnings {
		fmt.Fprintln(c.ErrOrStderr(), "warning:", w)
	}

	prefix := "imported"
	if res.DryRun {
		prefix = "would import"
	}
	fmt.Fprintf(out, "%s %d row(s) (skipped %d existing, %d missing blob)\n",
		prefix, imported, skippedExisting, skippedMissingBlob)
	for _, t := range res.Tables {
		fmt.Fprintf(out, "  %-14s imported=%d skipped_existing=%d skipped_missing_blob=%d\n",
			t.Table, t.Imported, t.SkippedExisting, t.SkippedMissingBlob)
	}
	return nil
}
