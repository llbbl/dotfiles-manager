package main

import (
	"bytes"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/llbbl/dotfiles-manager/internal/alias"
	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/llbbl/dotfiles-manager/internal/shellrc"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/spf13/cobra"
)

// newAliasCmd builds the parent `dfm alias` command and registers
// its add/remove/list subcommands. The helper is a shell-aware thin
// wrapper around the same atomic-append machinery as `dfm append`.
func newAliasCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alias",
		Short: "Manage shell aliases in tracked rc files",
	}
	cmd.AddCommand(newAliasAddCmd(), newAliasRemoveCmd(), newAliasListCmd())
	return cmd
}

// resolveAliasTarget resolves the target rc file and collapses its shell
// to the alias family ("fish" or "posix") used for quoting and parsing.
func resolveAliasTarget(shellFlag, fileFlag string) (string, string, error) {
	target, shell, err := shellrc.ResolveTarget(shellFlag, fileFlag)
	if err != nil {
		return "", "", err
	}
	return target, shellrc.Family(shell), nil
}

func newAliasAddCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		groupFlag string
		force     bool
		replace   bool
	)
	cmd := &cobra.Command{
		Use:   "add <name> <command>",
		Short: "Snapshot then append an alias definition to a tracked rc file",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			name, command := args[0], args[1]
			if !alias.ValidName.MatchString(name) {
				return exitf(exitResolveErr,
					"invalid alias name %q: must match [A-Za-z_][A-Za-z0-9_]*", name)
			}
			if command == "" {
				return exitf(exitResolveErr, "alias command must not be empty")
			}
			if replace && force {
				return exitf(exitResolveErr,
					"--replace and --force are mutually exclusive")
			}
			if groupFlag != "" && !alias.ValidGroup.MatchString(groupFlag) {
				return exitf(exitResolveErr,
					"invalid --group name %q: must match [A-Za-z][A-Za-z0-9_-]*", groupFlag)
			}

			target, family, err := resolveAliasTarget(shellFlag, fileFlag)
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

			existingCount := alias.Count(family, name, string(current))

			// Default path (no --replace, no --force): refuse to add a
			// duplicate. No snapshot, no audit, no mutation.
			if existingCount > 0 && !replace && !force {
				return exitf(exitAlreadyOrMiss,
					"alias '%s' already defined in %s (use --replace to overwrite, --force to append duplicate)",
					name, file.DisplayPath)
			}

			bodyLine := alias.Line(family, name, command)

			var (
				newContent  string
				subAction   = "append"
				blockAction string
			)
			switch {
			case replace && existingCount > 0:
				stripped, _ := alias.RemoveEverywhere(family, name, string(current))
				newContent, blockAction = alias.Upsert(stripped, groupFlag, bodyLine)
				subAction = "replace"
			case force && existingCount > 0:
				newContent, blockAction = alias.Upsert(string(current), groupFlag, bodyLine)
				subAction = "force-append"
			default:
				newContent, blockAction = alias.Upsert(string(current), groupFlag, bodyLine)
			}
			newBytes := []byte(newContent)
			bytesDelta := len(newBytes) - len(current)

			// Same secrets pre-flight as `append`: scan the full new
			// content so a credential can't be smuggled in via an alias
			// body. We deliberately reuse the same exit code + message
			// shape so the UX is uniform.
			res, scanErr := secrets.ScanReader(bytes.NewReader(newBytes))
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
					fmt.Fprintln(c.ErrOrStderr(),
						"alias add aborted: secrets detected (--force to override)")
					os.Exit(exitSecretsErr)
				}
				fmt.Fprintf(c.ErrOrStderr(),
					"warning: %d secret finding(s) in alias content; proceeding due to --force\n",
					len(res.Findings))
			}

			mgr, mgrErr := newSnapshotManager(c.Context(), s)
			if mgrErr != nil {
				return fmt.Errorf("snapshot manager: %w", mgrErr)
			}
			// `sub_action` is the user-intent path (append/replace/
			// force-append). `block_action` is the block-level effect
			// (created vs appended-into vs replaced). For --replace we always tag
			// "replaced" rather than relying on the upsert helper's view,
			// because the block-level outcome is conceptually a substitution
			// even when the underlying mechanic was "create then insert".
			if subAction == "replace" {
				blockAction = "replaced"
			}
			groupLabel := groupFlag
			if groupLabel == "" {
				groupLabel = "aliases"
			}

			// Privacy: NEVER include the command body. Name + shell +
			// group + counts + hashes only.
			if _, err := snapshot.WriteTracked(c.Context(), mgr, s, canonical, file, newBytes, info.Mode().Perm(), "alias.add", map[string]any{
				"alias_name":   name,
				"shell":        family,
				"group":        groupLabel,
				"sub_action":   subAction,
				"block_action": blockAction,
				"bytes_delta":  bytesDelta,
			}); err != nil {
				return err
			}

			switch subAction {
			case "replace":
				fmt.Fprintf(c.OutOrStdout(), "replaced alias '%s' in %s\n", name, file.DisplayPath)
			case "force-append":
				fmt.Fprintf(c.OutOrStdout(),
					"appended duplicate alias '%s' to %s (forced)\n", name, file.DisplayPath)
			default:
				fmt.Fprintf(c.OutOrStdout(), "added alias '%s' to %s\n", name, file.DisplayPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	cmd.Flags().StringVar(&groupFlag, "group", "", "place the alias inside the named shared block")
	cmd.Flags().BoolVar(&force, "force", false, "add even if secrets are detected OR alias already exists")
	cmd.Flags().BoolVar(&replace, "replace", false, "overwrite any existing definition of this alias")
	return cmd
}

func newAliasRemoveCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
	)
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove all alias definitions for <name> from a tracked rc file",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			if !alias.ValidName.MatchString(name) {
				return exitf(exitResolveErr,
					"invalid alias name %q: must match [A-Za-z_][A-Za-z0-9_]*", name)
			}

			target, family, err := resolveAliasTarget(shellFlag, fileFlag)
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

			stripped, rc := alias.RemoveEverywhere(family, name, string(current))
			removed := rc.LegacyBlocks + rc.SharedEvicted + rc.BareLines
			if removed == 0 {
				fmt.Fprintf(c.ErrOrStderr(),
					"alias '%s' not found in %s\n", name, file.DisplayPath)
				os.Exit(exitNotFound)
			}

			newContent := []byte(stripped)

			mgr, mgrErr := newSnapshotManager(c.Context(), s)
			if mgrErr != nil {
				return fmt.Errorf("snapshot manager: %w", mgrErr)
			}
			// `lines_removed` is the user-visible total. The breakdown
			// fields let audit consumers tell where the entries came
			// from: legacy per-alias blocks, evictions from shared
			// blocks, stray bare lines, and how many shared blocks went
			// empty as a side-effect.
			if _, err := snapshot.WriteTracked(c.Context(), mgr, s, canonical, file, newContent, info.Mode().Perm(), "alias.remove", map[string]any{
				"alias_name":             name,
				"shell":                  family,
				"lines_removed":          removed,
				"blocks_removed":         rc.LegacyBlocks,
				"bare_lines_removed":     rc.BareLines,
				"shared_block_evictions": rc.SharedEvicted,
				"empty_blocks_dropped":   rc.EmptiedBlocks,
			}); err != nil {
				return err
			}

			fmt.Fprintf(c.OutOrStdout(),
				"removed %d alias line(s) for '%s' from %s\n", removed, name, file.DisplayPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	return cmd
}

func newAliasListCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List aliases parsed from a tracked rc file (best-effort)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			target, family, err := resolveAliasTarget(shellFlag, fileFlag)
			if err != nil {
				return err
			}

			s, err := openStore(c.Context())
			if err != nil {
				return err
			}
			defer s.Close()

			// list is read-only and must work even when the rc file isn't
			// tracked — we just append a hint at the bottom in that case.
			displayPath := target
			tracked := true
			if file, _, rerr := resolveTracked(c.Context(), s, target); rerr != nil {
				tracked = false
			} else {
				displayPath = file.DisplayPath
			}

			data, err := os.ReadFile(target)
			if err != nil {
				return fmt.Errorf("read %s: %w", target, err)
			}

			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tCOMMAND\tFILE")
			for _, e := range alias.Parse(family, string(data)) {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, e.Command, displayPath)
			}
			tw.Flush()

			if !tracked {
				fmt.Fprintf(c.OutOrStdout(),
					"note: %s is not tracked; aliases shown but not under management\n",
					target)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	return cmd
}
