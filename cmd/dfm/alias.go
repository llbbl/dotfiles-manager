package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/spf13/cobra"
)

// aliasNameRe enforces the strict, portable subset of alias names that
// work across bash, zsh, fish, and POSIX sh: a leading letter or
// underscore followed by letters, digits, or underscores. We deliberately
// reject hyphens — they're legal in some shells but not portable.
var aliasNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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

// detectShell returns the basename of $SHELL, lowercased. Empty if unset.
func detectShell() string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(sh))
}

// rcFileForShell maps a shell name to its conventional rc file. Anything
// unrecognised falls back to ~/.profile (POSIX-compatible).
func rcFileForShell(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	switch shell {
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "zsh":
		return filepath.Join(home, ".zshrc"), nil
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish"), nil
	default:
		return filepath.Join(home, ".profile"), nil
	}
}

// shellFamily collapses a raw shell name to one of "fish" or "posix"
// (bash, zsh, sh, profile). Used for picking quoting + regex syntax.
func shellFamily(shell string) string {
	if shell == "fish" {
		return "fish"
	}
	return "posix"
}

// resolveAliasTarget figures out which file we should be mutating given
// the user's optional --shell / --file overrides plus $SHELL. The first
// return value is the absolute file path; the second is the shell family
// ("fish" or "posix") used for quoting and parsing.
func resolveAliasTarget(shellFlag, fileFlag string) (string, string, error) {
	if fileFlag != "" {
		abs, err := filepath.Abs(fileFlag)
		if err != nil {
			return "", "", fmt.Errorf("resolve --file: %w", err)
		}
		shell := shellFlag
		if shell == "" {
			// Infer family from filename when --file is used without --shell.
			if strings.HasSuffix(abs, ".fish") {
				shell = "fish"
			} else {
				shell = "posix"
			}
		}
		return abs, shellFamily(shell), nil
	}
	shell := shellFlag
	if shell == "" {
		shell = detectShell()
	}
	path, err := rcFileForShell(shell)
	if err != nil {
		return "", "", err
	}
	return path, shellFamily(shell), nil
}

// buildAliasLine assembles the exact text we will append for an alias
// definition, including the trailing newline. Quoting is intentionally
// conservative — both branches wrap the body in single quotes so the
// shell does no expansion at runtime.
//
// POSIX shells (bash/zsh/sh): the only character that needs special
// handling inside single quotes is the single quote itself, which can't
// be escaped at all — we close the quote, emit a literal quote via
// '\'', and reopen.
//
// Fish: inside single quotes only \\ and \' have meaning; everything
// else is literal. So we escape backslash first, then single quote.
func buildAliasLine(family, name, command string) string {
	if family == "fish" {
		escaped := strings.ReplaceAll(command, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `'`, `\'`)
		return fmt.Sprintf("alias %s '%s'\n", name, escaped)
	}
	// posix: replace ' with '\''
	escaped := strings.ReplaceAll(command, `'`, `'\''`)
	return fmt.Sprintf("alias %s='%s'\n", name, escaped)
}

// aliasMatchRe returns a compiled regex matching any line that defines
// the given alias name in the given shell family. Used by `remove` and
// indirectly by `list` (which has its own capture-style regex). Retained
// as the legacy bare-line matcher after dfm switched to emitting fenced
// blocks via buildAliasBlock — still used to detect, replace, and strip
// un-fenced definitions written by older dfm versions or by hand.
func aliasMatchRe(family, name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	if family == "fish" {
		return regexp.MustCompile(`^\s*alias\s+` + q + `\s+`)
	}
	return regexp.MustCompile(`^\s*alias\s+` + q + `\s*=`)
}

// buildAliasBlock returns the fenced multi-line block for an alias. The
// block has the shape:
//
//	# >>> dfm:alias <name> >>>
//	alias <name>='<cmd>'
//	# <<< dfm:alias <name> <<<
//
// and includes a trailing newline so it can be concatenated onto existing
// content without further fixup. The body line is delegated to
// buildAliasLine so quoting rules for posix vs fish stay in one place.
// The bare-line form (buildAliasLine) is kept for use in the test suite
// and as the legacy fallback recognised by aliasMatchRe.
//
// Comment syntax is `#` in both POSIX shells and fish, so the same fence
// works across families.
func buildAliasBlock(family, name, command string) string {
	body := buildAliasLine(family, name, command) // already trailing \n
	return fmt.Sprintf("# >>> dfm:alias %s >>>\n%s# <<< dfm:alias %s <<<\n",
		name, body, name)
}

// aliasBlockRe matches a complete fenced block for the given alias name
// (open fence, one body line, close fence). The match is tolerant of
// leading whitespace on the fence lines and is multi-line aware. Used
// by add (dup-detect + replace) and remove. The trailing newline after
// the close fence is optional so a block flush against EOF still matches.
func aliasBlockRe(name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	return regexp.MustCompile(
		`(?m)^[ \t]*# >>> dfm:alias ` + q + ` >>>\n` +
			`[^\n]*\n` +
			`[ \t]*# <<< dfm:alias ` + q + ` <<<\n?`)
}

func newAliasAddCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		force     bool
		replace   bool
	)
	cmd := &cobra.Command{
		Use:   "add <name> <command>",
		Short: "Snapshot then append an alias definition to a tracked rc file",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			name, command := args[0], args[1]
			if !aliasNameRe.MatchString(name) {
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

			block := buildAliasBlock(family, name, command)
			appendBytes := []byte(block)

			current, err := os.ReadFile(canonical)
			if err != nil {
				return fmt.Errorf("read %s: %w", canonical, err)
			}
			info, err := os.Stat(canonical)
			if err != nil {
				return fmt.Errorf("stat %s: %w", canonical, err)
			}

			// Detect any existing definition(s) of this alias so we can
			// reject duplicate adds by default, swap them under --replace,
			// or stack them under --force. An alias counts as "defined"
			// if EITHER a fenced dfm block or a legacy bare-line `alias
			// <name>=...` is present — that way a user who hand-wrote an
			// alias before adopting dfm is still protected from silent
			// duplication on `dfm alias add`.
			matchRe := aliasMatchRe(family, name)
			blockRe := aliasBlockRe(name)
			blockMatches := blockRe.FindAllStringIndex(string(current), -1)
			bareMatches := 0
			for ln := range strings.SplitSeq(string(current), "\n") {
				if matchRe.MatchString(ln) {
					bareMatches++
				}
			}
			// A bare line that's actually the body of a fenced block
			// would be double-counted; subtract one bare hit per fenced
			// block so the dup-detect logic doesn't get confused by its
			// own emission.
			bareOutsideBlocks := max(bareMatches-len(blockMatches), 0)
			existingMatches := len(blockMatches) + bareOutsideBlocks

			// Default path (no --replace, no --force): refuse to add a
			// duplicate. No snapshot, no audit, no mutation — pure no-op
			// rejection so scripted callers can detect & branch cleanly.
			if existingMatches > 0 && !replace && !force {
				return exitf(exitAlreadyOrMiss,
					"alias '%s' already defined in %s (use --replace to overwrite, --force to append duplicate)",
					name, file.DisplayPath)
			}

			// Build newContent + decide the audit action discriminator.
			// Three branches:
			//   - replace with existing matches: substitute the first
			//     fenced block (or first bare line if no fenced block
			//     exists) with the new fenced block, drop any remaining
			//     definitions of the same alias in either form.
			//   - replace with no existing matches: behave like append
			//     (idempotent for scripts).
			//   - append (default or --force): append the new fenced
			//     block verbatim.
			var newContent []byte
			// "sub_action" not "action": the audit logger reserves the
			// top-level "action" key for the event name (e.g. "alias.add")
			// and overwrites any caller-supplied value, so we use
			// sub_action as the within-event discriminator.
			subAction := "append"
			bytesDelta := 0
			if replace && existingMatches > 0 {
				// Strategy: strip ALL existing definitions (fenced and
				// bare) from the file, then insert exactly one new
				// fenced block. The insertion point is, in priority
				// order: where the first existing fenced block lived,
				// otherwise where the first legacy bare line lived,
				// otherwise at EOF. This guarantees we end up with
				// exactly one block and zero stray bare lines, even if
				// the user hand-edited around dfm.
				const placeholder = "\x00DFM_ALIAS_BLOCK_INSERT\x00"
				blockNoTrail := strings.TrimSuffix(block, "\n")

				// Phase 1: replace the first fenced block (if any) with
				// the placeholder, drop the rest entirely.
				working := string(current)
				placedViaBlock := false
				if len(blockMatches) > 0 {
					working = blockRe.ReplaceAllStringFunc(working, func(_ string) string {
						if !placedViaBlock {
							placedViaBlock = true
							return placeholder + "\n"
						}
						return ""
					})
				}

				// Phase 2: walk lines, drop bare matches. If the
				// placeholder isn't already in the text (no fenced
				// blocks existed), emit it at the position of the
				// first bare match.
				workingLines := strings.Split(working, "\n")
				rewritten := make([]string, 0, len(workingLines))
				placedViaBare := placedViaBlock
				for _, ln := range workingLines {
					if matchRe.MatchString(ln) {
						if !placedViaBare {
							rewritten = append(rewritten, placeholder)
							placedViaBare = true
						}
						continue
					}
					rewritten = append(rewritten, ln)
				}
				joined := strings.Join(rewritten, "\n")

				// Phase 3: substitute the placeholder with the real
				// block content. The placeholder was inserted as its
				// own line; replacing the placeholder string (not the
				// whole line) preserves surrounding newlines correctly.
				finalText := strings.Replace(joined, placeholder, blockNoTrail, 1)
				newContent = []byte(finalText)
				subAction = "replace"
				bytesDelta = len(newContent) - len(current)
			} else {
				// append path (covers: no matches at all, --replace with
				// nothing to replace, and --force on top of existing)
				newContent = append(append([]byte{}, current...), appendBytes...)
				if force && existingMatches > 0 {
					subAction = "force-append"
				}
			}

			// Same secrets pre-flight as `append`: scan the full new
			// content so a credential can't be smuggled in via an alias
			// body. We deliberately reuse the same exit code + message
			// shape so the UX is uniform.
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

			// Privacy: NEVER include the command body. Name + shell +
			// counts + hashes only. `sub_action` discriminates which
			// branch we took so audit consumers can distinguish replace
			// vs append vs forced-duplicate at query time. (Top-level
			// "action" is reserved by the audit logger for the event
			// name "alias.add".)
			fields := map[string]any{
				"display_path": file.DisplayPath,
				"file_id":      file.ID,
				"snapshot_id":  snap.ID,
				"alias_name":   name,
				"shell":        family,
				"sub_action":   subAction,
				"old_hash":     file.LastHash,
				"new_hash":     newHash,
			}
			if subAction == "replace" {
				fields["bytes_delta"] = bytesDelta
			} else {
				fields["bytes_appended"] = len(appendBytes)
			}
			audit.Log(c.Context(), "alias.add", fields)

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
			if !aliasNameRe.MatchString(name) {
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

			// Two-phase removal: first strip any fenced dfm blocks for
			// this alias (the modern form), then sweep any remaining
			// legacy bare-line definitions. Both phases contribute to
			// the audit count.
			blockRe := aliasBlockRe(name)
			blocksRemoved := len(blockRe.FindAllStringIndex(string(current), -1))
			afterBlocks := blockRe.ReplaceAllString(string(current), "")

			re := aliasMatchRe(family, name)

			// We split deliberately on '\n' (not bufio.Scanner) so we
			// preserve a trailing-newline-or-not distinction faithfully:
			// strings.Split("a\nb\n", "\n") yields ["a","b",""], and
			// joining the kept slice with "\n" reproduces the original
			// terminator state when no lines are removed from the tail.
			lines := strings.Split(afterBlocks, "\n")
			kept := make([]string, 0, len(lines))
			bareRemoved := 0
			for _, ln := range lines {
				if re.MatchString(ln) {
					bareRemoved++
					continue
				}
				kept = append(kept, ln)
			}
			removed := blocksRemoved + bareRemoved
			if removed == 0 {
				fmt.Fprintf(c.ErrOrStderr(),
					"alias '%s' not found in %s\n", name, file.DisplayPath)
				os.Exit(exitNotFound)
			}

			newContent := []byte(strings.Join(kept, "\n"))

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

			// `lines_removed` is the total count across both removal
			// phases (fenced blocks counted as one removal each, plus
			// any legacy bare lines). The phase breakdown is preserved
			// as `blocks_removed` / `bare_lines_removed` for audit
			// consumers that want to distinguish managed from legacy
			// entries.
			audit.Log(c.Context(), "alias.remove", map[string]any{
				"display_path":       file.DisplayPath,
				"file_id":            file.ID,
				"snapshot_id":        snap.ID,
				"alias_name":         name,
				"shell":              family,
				"lines_removed":      removed,
				"blocks_removed":     blocksRemoved,
				"bare_lines_removed": bareRemoved,
				"old_hash":           file.LastHash,
				"new_hash":           newHash,
			})

			fmt.Fprintf(c.OutOrStdout(),
				"removed %d alias line(s) for '%s' from %s\n", removed, name, file.DisplayPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	return cmd
}

// aliasListPosixRe and aliasListFishRe are intentionally best-effort:
// they recognise the common single-line `alias name='body'` forms but
// don't try to handle escaped quotes or multi-line definitions. The
// list command swallows any line that doesn't match.
var (
	aliasListPosixRe = regexp.MustCompile(`^\s*alias\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(['"])(.+)(['"])\s*$`)
	aliasListFishRe  = regexp.MustCompile(`^\s*alias\s+([A-Za-z_][A-Za-z0-9_]*)\s+(['"])(.+)(['"])\s*$`)
)

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

			re := aliasListPosixRe
			if family == "fish" {
				re = aliasListFishRe
			}

			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tCOMMAND\tFILE")
			for ln := range strings.SplitSeq(string(data), "\n") {
				m := re.FindStringSubmatch(ln)
				if m == nil {
					continue
				}
				// m[1]=name, m[3]=body. We deliberately don't un-escape.
				fmt.Fprintf(tw, "%s\t%s\t%s\n", m[1], m[3], displayPath)
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
