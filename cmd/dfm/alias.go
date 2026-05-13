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

// aliasGroupNameRe is what we accept for `--group <name>`. We're stricter
// than alias names: must start with a letter (so the fence comment never
// gets confused for a directive), allow hyphens (group names are display
// labels, not shell identifiers), keep regex-special chars out. This lets
// us interpolate the value directly into a fence regex without
// QuoteMeta gymnastics.
var aliasGroupNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

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
// as the legacy bare-line matcher: in the shared-block era this matches
// both body lines inside a managed block and stray bare lines outside.
func aliasMatchRe(family, name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	if family == "fish" {
		return regexp.MustCompile(`^\s*alias\s+` + q + `\s+`)
	}
	return regexp.MustCompile(`^\s*alias\s+` + q + `\s*=`)
}

// aliasLegacyBlockRe matches the OLD per-alias fenced block format
// emitted prior to the shared-block migration (dfm-yda). We still
// recognise these so dup-detect, remove, and --replace continue to
// work on rc files written by older dfm versions. New writes never
// produce this shape.
func aliasLegacyBlockRe(name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	return regexp.MustCompile(
		`(?m)^[ \t]*# >>> dfm:alias ` + q + ` >>>\n` +
			`[^\n]*\n` +
			`[ \t]*# <<< dfm:alias ` + q + ` <<<\n?`)
}

// aliasDefaultBlockRe matches the entire shared default block:
// `# >>> dfm:aliases >>>` … `# <<< dfm:aliases <<<`. Submatch 1 is the
// body between the fences (may be empty). Trailing newline is optional
// so EOF-flush blocks still match.
func aliasDefaultBlockRe() *regexp.Regexp {
	return regexp.MustCompile(
		`(?ms)^[ \t]*# >>> dfm:aliases >>>\n` +
			`(.*?)` +
			`^[ \t]*# <<< dfm:aliases <<<\n?`)
}

// aliasGroupBlockRe matches the entire shared named-group block:
// `# >>> dfm:group <name> >>>` … `# <<< dfm:group <name> <<<`. Submatch
// 1 is the body. The group name has already been validated against
// aliasGroupNameRe by the caller so it's safe to interpolate as a
// literal (no regex metacharacters survive that validator).
func aliasGroupBlockRe(group string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?ms)^[ \t]*# >>> dfm:group ` + group + ` >>>\n` +
			`(.*?)` +
			`^[ \t]*# <<< dfm:group ` + group + ` <<<\n?`)
}

// aliasAnyGroupBlockRe matches any named-group block regardless of name.
// Used by dup-detect to scan every group block in one pass. Go's RE2
// flavor doesn't support backreferences, so we can't pin the close-fence
// name to the open-fence name in a single regex. We accept any close
// fence and then verify the open/close names agree at the call site. The
// open-fence name is captured in submatch 1, body in submatch 2, the
// close-fence name in submatch 3.
func aliasAnyGroupBlockRe() *regexp.Regexp {
	return regexp.MustCompile(
		`(?ms)^[ \t]*# >>> dfm:group ([A-Za-z][A-Za-z0-9_-]*) >>>\n` +
			`(.*?)` +
			`^[ \t]*# <<< dfm:group ([A-Za-z][A-Za-z0-9_-]*) <<<\n?`)
}

// renderAliasesBlock builds the full default shared block from a list
// of pre-rendered body lines (each already terminated by \n). Returns
// the block with a trailing newline after the close fence.
func renderAliasesBlock(bodyLines []string) string {
	var b strings.Builder
	b.WriteString("# >>> dfm:aliases >>>\n")
	for _, ln := range bodyLines {
		b.WriteString(ln)
	}
	b.WriteString("# <<< dfm:aliases <<<\n")
	return b.String()
}

// renderGroupBlock builds the full named-group shared block. The group
// name is interpolated verbatim; callers must have validated it against
// aliasGroupNameRe first.
func renderGroupBlock(group string, bodyLines []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# >>> dfm:group %s >>>\n", group)
	for _, ln := range bodyLines {
		b.WriteString(ln)
	}
	fmt.Fprintf(&b, "# <<< dfm:group %s <<<\n", group)
	return b.String()
}

// extractBlockBodyLines splits a captured block body (the submatch from
// aliasDefaultBlockRe/aliasGroupBlockRe) into its constituent newline-
// terminated lines. Empty trailing element from the final \n is dropped.
func extractBlockBodyLines(body string) []string {
	if body == "" {
		return nil
	}
	parts := strings.SplitAfter(body, "\n")
	// SplitAfter keeps the separator; trim a trailing empty element if
	// body didn't end on a newline (it always should, but be defensive).
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// countAliasDefinitions scans `content` for every place the alias `name`
// is currently defined: inside a legacy per-alias block, inside the
// shared default block, inside any named-group block, or as a bare line
// not belonging to any of the above. The total is what gates the
// duplicate-rejection check.
func countAliasDefinitions(family, name, content string) int {
	matchRe := aliasMatchRe(family, name)
	legacyRe := aliasLegacyBlockRe(name)
	defaultRe := aliasDefaultBlockRe()
	groupRe := aliasAnyGroupBlockRe()

	legacyBlocks := len(legacyRe.FindAllStringIndex(content, -1))

	// Count body-line hits inside each managed block.
	sharedHits := 0
	if m := defaultRe.FindStringSubmatch(content); m != nil {
		body := m[1]
		for _, ln := range extractBlockBodyLines(body) {
			if matchRe.MatchString(ln) {
				sharedHits++
			}
		}
	}
	for _, sub := range groupRe.FindAllStringSubmatch(content, -1) {
		// Skip malformed blocks where the open and close names don't
		// agree (Go's RE2 can't pin them in the regex; we check here).
		if sub[1] != sub[3] {
			continue
		}
		body := sub[2]
		for _, ln := range extractBlockBodyLines(body) {
			if matchRe.MatchString(ln) {
				sharedHits++
			}
		}
	}

	// Bare-line scan over the whole file. We then subtract the lines
	// that we already counted inside legacy blocks (1 body line each)
	// and shared blocks (sharedHits, already counted explicitly).
	bareTotal := 0
	for ln := range strings.SplitSeq(content, "\n") {
		if matchRe.MatchString(ln) {
			bareTotal++
		}
	}
	bareOutside := max(bareTotal-legacyBlocks-sharedHits, 0)

	return legacyBlocks + sharedHits + bareOutside
}

// removeAliasEverywhere strips every definition of `name` from `content`
// across all storage forms: legacy per-alias blocks, body lines in the
// shared default block, body lines in every named-group block, and any
// remaining bare lines. Empty shared blocks are dropped entirely. The
// counts in the returned struct feed the audit log.
type removalCounts struct {
	legacyBlocks   int // number of legacy per-alias blocks removed
	sharedEvicted  int // body lines pulled out of shared blocks
	emptiedBlocks  int // shared blocks that ended up empty and were dropped
	bareLines      int // stray un-fenced bare lines removed
}

func removeAliasEverywhere(family, name, content string) (string, removalCounts) {
	var rc removalCounts
	matchRe := aliasMatchRe(family, name)

	// Phase 1: drop every legacy per-alias block for this name.
	legacyRe := aliasLegacyBlockRe(name)
	rc.legacyBlocks = len(legacyRe.FindAllStringIndex(content, -1))
	content = legacyRe.ReplaceAllString(content, "")

	// Phase 2a: rewrite the default shared block (if present), pruning
	// any body lines that define this alias. If the block ends up empty,
	// remove it entirely.
	defaultRe := aliasDefaultBlockRe()
	content = defaultRe.ReplaceAllStringFunc(content, func(full string) string {
		m := defaultRe.FindStringSubmatch(full)
		body := m[1]
		kept := make([]string, 0)
		for _, ln := range extractBlockBodyLines(body) {
			if matchRe.MatchString(ln) {
				rc.sharedEvicted++
				continue
			}
			kept = append(kept, ln)
		}
		if len(kept) == 0 {
			rc.emptiedBlocks++
			return ""
		}
		return renderAliasesBlock(kept)
	})

	// Phase 2b: same treatment for every named-group block.
	groupRe := aliasAnyGroupBlockRe()
	content = groupRe.ReplaceAllStringFunc(content, func(full string) string {
		m := groupRe.FindStringSubmatch(full)
		// Mismatched open/close fence names: leave the text alone.
		if m[1] != m[3] {
			return full
		}
		groupName := m[1]
		body := m[2]
		kept := make([]string, 0)
		for _, ln := range extractBlockBodyLines(body) {
			if matchRe.MatchString(ln) {
				rc.sharedEvicted++
				continue
			}
			kept = append(kept, ln)
		}
		if len(kept) == 0 {
			rc.emptiedBlocks++
			return ""
		}
		return renderGroupBlock(groupName, kept)
	})

	// Phase 3: sweep any remaining bare lines (legacy un-fenced entries
	// from before dfm fenced anything).
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if matchRe.MatchString(ln) {
			rc.bareLines++
			continue
		}
		kept = append(kept, ln)
	}
	content = strings.Join(kept, "\n")

	return content, rc
}

// upsertAliasIntoBlock inserts `bodyLine` into the target shared block,
// creating the block at EOF if it doesn't yet exist. `group` is "" for
// the default block, otherwise the validated group name.
func upsertAliasIntoBlock(content, group, bodyLine string) (string, string) {
	var re *regexp.Regexp
	if group == "" {
		re = aliasDefaultBlockRe()
	} else {
		re = aliasGroupBlockRe(group)
	}
	if loc := re.FindStringSubmatchIndex(content); loc != nil {
		// loc[2]/loc[3] is the body capture range.
		bodyStart, bodyEnd := loc[2], loc[3]
		body := content[bodyStart:bodyEnd]
		lines := extractBlockBodyLines(body)
		lines = append(lines, bodyLine)
		var rebuilt string
		if group == "" {
			rebuilt = renderAliasesBlock(lines)
		} else {
			rebuilt = renderGroupBlock(group, lines)
		}
		return content[:loc[0]] + rebuilt + content[loc[1]:], "appended"
	}
	// No existing block: append a fresh one. Guarantee a newline
	// separator if the file doesn't already end in \n (or is empty,
	// in which case we want nothing before the block).
	prefix := content
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	var block string
	if group == "" {
		block = renderAliasesBlock([]string{bodyLine})
	} else {
		block = renderGroupBlock(group, []string{bodyLine})
	}
	return prefix + block, "created"
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
			if groupFlag != "" && !aliasGroupNameRe.MatchString(groupFlag) {
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

			existingCount := countAliasDefinitions(family, name, string(current))

			// Default path (no --replace, no --force): refuse to add a
			// duplicate. No snapshot, no audit, no mutation.
			if existingCount > 0 && !replace && !force {
				return exitf(exitAlreadyOrMiss,
					"alias '%s' already defined in %s (use --replace to overwrite, --force to append duplicate)",
					name, file.DisplayPath)
			}

			bodyLine := buildAliasLine(family, name, command)

			var (
				newContent  string
				subAction   = "append"
				blockAction string
			)
			switch {
			case replace && existingCount > 0:
				stripped, _ := removeAliasEverywhere(family, name, string(current))
				newContent, blockAction = upsertAliasIntoBlock(stripped, groupFlag, bodyLine)
				subAction = "replace"
			case force && existingCount > 0:
				newContent, blockAction = upsertAliasIntoBlock(string(current), groupFlag, bodyLine)
				subAction = "force-append"
			default:
				newContent, blockAction = upsertAliasIntoBlock(string(current), groupFlag, bodyLine)
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
			f := file
			snap, err := mgr.Snapshot(c.Context(), canonical, &f, snapshot.ReasonPreEdit)
			if err != nil {
				return fmt.Errorf("pre-edit snapshot: %w", err)
			}

			if err := atomicWrite(canonical, newBytes, info.Mode().Perm()); err != nil {
				return fmt.Errorf("write %s: %w", canonical, err)
			}

			sum := sha256.Sum256(newBytes)
			newHash := hex.EncodeToString(sum[:])

			if _, err := s.DB().ExecContext(c.Context(),
				`UPDATE tracked_files SET last_hash = ? WHERE id = ?`, newHash, file.ID); err != nil {
				return fmt.Errorf("update tracked_files: %w", err)
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
			fields := map[string]any{
				"display_path": file.DisplayPath,
				"file_id":      file.ID,
				"snapshot_id":  snap.ID,
				"alias_name":   name,
				"shell":        family,
				"group":        groupLabel,
				"sub_action":   subAction,
				"block_action": blockAction,
				"old_hash":     file.LastHash,
				"new_hash":     newHash,
				"bytes_delta":  bytesDelta,
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

			stripped, rc := removeAliasEverywhere(family, name, string(current))
			removed := rc.legacyBlocks + rc.sharedEvicted + rc.bareLines
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

			// `lines_removed` is the user-visible total. The breakdown
			// fields let audit consumers tell where the entries came
			// from: legacy per-alias blocks, evictions from shared
			// blocks, stray bare lines, and how many shared blocks went
			// empty as a side-effect.
			audit.Log(c.Context(), "alias.remove", map[string]any{
				"display_path":           file.DisplayPath,
				"file_id":                file.ID,
				"snapshot_id":            snap.ID,
				"alias_name":             name,
				"shell":                  family,
				"lines_removed":          removed,
				"blocks_removed":         rc.legacyBlocks,
				"bare_lines_removed":     rc.bareLines,
				"shared_block_evictions": rc.sharedEvicted,
				"empty_blocks_dropped":   rc.emptiedBlocks,
				"old_hash":               file.LastHash,
				"new_hash":               newHash,
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
