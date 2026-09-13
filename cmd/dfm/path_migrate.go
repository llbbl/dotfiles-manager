package main

import (
	"fmt"
	"os"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// activeConfigPath resolves the config file this invocation reads and
// writes: the --config flag if given, otherwise the XDG default.
func activeConfigPath() (string, error) {
	if flagConfigPath != "" {
		return flagConfigPath, nil
	}
	return config.DefaultPath()
}

// removePathBlocks splices every managed block out of content, last
// first so the earlier byte offsets stay valid. A blank line that an
// add wrote as a separator in front of a block goes with it, so the
// surrounding file comes back to the bytes it had before dfm touched it.
func removePathBlocks(content []byte, entries []PathManagedEntry) []byte {
	out := content
	for i := len(entries) - 1; i >= 0; i-- {
		start, end := entries[i].BlockStart, entries[i].BlockEnd
		// The separator is "\n" on its own, or "\r\n" on a CRLF file.
		if start >= 2 && out[start-1] == '\n' {
			switch {
			case out[start-2] == '\n':
				start--
			case out[start-2] == '\r' && start >= 3 && out[start-3] == '\n':
				start -= 2
			}
		}
		out = append(out[:start:start], out[end:]...)
	}
	return out
}

func newPathMigrateCmd() *cobra.Command {
	var (
		shellFlag string
		dryRun    bool
		yes       bool
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Move managed PATH entries out of the rc file into the generated fragment",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runPathMigrate(c, shellFlag, dryRun, yes, force)
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print every step without writing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "migrate without prompting (non-interactive)")
	cmd.Flags().BoolVar(&force, "force", false, "track a file even if it trips the secret scan")
	return cmd
}

// runPathMigrate makes the fragment the single home for managed PATH
// entries. The steps are ordered so a failure never leaves the entries
// with no copy on disk: write the fragment, install the hooks that
// source it, track it, only then strip the rc file, and flip the switch
// last.
func runPathMigrate(c *cobra.Command, shellFlag string, dryRun, yes, force bool) error {
	out := c.OutOrStdout()

	if pathUseFragment(c.Context()) {
		fmt.Fprintln(out, "already migrated: managed PATH entries live in the fragment")
		return nil
	}

	shell, err := resolveHookShell("path migrate", shellFlag)
	if err != nil {
		return err
	}
	rc, err := rcFileForShell(shell)
	if err != nil {
		return err
	}
	_, family, err := pathHookFiles(shell)
	if err != nil {
		return err
	}
	fragment := config.FragmentPath(pathFragmentName(family))
	cfgPath, err := activeConfigPath()
	if err != nil {
		return err
	}

	rcData, err := os.ReadFile(rc)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", rc, err)
	}
	blocks := len(findPathManagedEntries(rcData))

	if dryRun {
		if err := runPathHook(c, shellFlag, true, force, true); err != nil {
			return err
		}
		fmt.Fprintf(out, "would track %s\n", fragment)
		fmt.Fprintf(out, "would strip %d managed block(s) from %s\n", blocks, rc)
		fmt.Fprintf(out, "would set path.use_fragment = true in %s\n", cfgPath)
		return nil
	}

	if !yes {
		if !isInteractiveStdin(c.InOrStdin()) {
			return exitf(exitResolveErr,
				"path migrate: non-interactive shell — pass --yes to migrate or --dry-run to preview")
		}
		fmt.Fprintf(c.ErrOrStderr(), "Move %d managed PATH block(s) from %s into %s.\n",
			blocks, rc, fragment)
		ok, perr := promptApply(c.ErrOrStderr(), c.InOrStdin())
		if perr != nil {
			return fmt.Errorf("read confirmation: %w", perr)
		}
		if !ok {
			fmt.Fprintln(out, "declined — no changes written.")
			return nil
		}
	}

	// Writes the fragment from the rc file's blocks, then installs the
	// hooks that source it.
	if err := runPathHook(c, shellFlag, false, force, true); err != nil {
		return err
	}

	s, err := openStore(c.Context())
	if err != nil {
		return err
	}
	defer s.Close()

	// The fragment is generated output until this point; from here it
	// holds the user's configuration, so it is tracked, snapshotted and
	// mirrored like any other dotfile.
	if _, _, terr := resolveTracked(c.Context(), s, fragment); terr != nil {
		code, err := runTrackOne(c, fragment, trackOneOptions{Force: force})
		if err != nil {
			return err
		}
		if code != 0 {
			return exitf(code,
				"path migrate: could not track %s. Re-run with --force to migrate anyway", fragment)
		}
	}

	// The switch has to flip before the rc file is stripped. The guard
	// that stops hook install regenerating the fragment keys off this
	// flag, so a crash between the two would leave the blocks gone from
	// the rc file and the fragment still regenerable — from a file that
	// no longer has them. Failing the other way round only leaves the
	// blocks in both places, which is what the pre-migrate state is.
	cfg := config.FromContext(c.Context())
	if cfg == nil {
		if cfg, err = config.Load(cfgPath); err != nil {
			return err
		}
	}
	cfg.Path.UseFragment = true
	saved := *cfg
	migrated, envPath, serr := config.SaveKeepingFileToken(cfgPath, &saved)
	if serr != nil {
		return serr
	}
	if migrated {
		fmt.Fprint(out, config.TokenMigrationNotice(envPath))
	}
	fmt.Fprintf(out, "set path.use_fragment = true in %s\n", cfgPath)

	if blocks > 0 {
		target, display := rc, rc
		if canonical, d, rerr := tracker.Resolve(rc); rerr == nil {
			target, display = canonical, d
		}
		current, rerr := os.ReadFile(target)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", target, rerr)
		}
		stripped := removePathBlocks(current, findPathManagedEntries(current))
		if _, err := writeTrackedEdit(c, s, "path migrate", rc, stripped, "path.migrate",
			map[string]any{"blocks": blocks, "fragment": fragment}, force); err != nil {
			return err
		}
		fmt.Fprintf(out, "stripped %d managed block(s) from %s\n", blocks, display)
	}
	return nil
}
