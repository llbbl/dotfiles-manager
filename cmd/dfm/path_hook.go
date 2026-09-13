package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/fsx"
	"github.com/llbbl/dotfiles-manager/internal/store"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// The hook uses a fixed marker, not the rotating id the dfm:path blocks
// carry: there is exactly one hook per file and no content to hash.
const (
	pathHookOpenMarker  = "# >>> dfm:env >>>"
	pathHookCloseMarker = "# <<< dfm:env <<<"
)

// pathHookBlockRe matches the hook block together with the single
// newline the installer writes in front of it, so a remove restores the
// file's original bytes exactly.
var pathHookBlockRe = regexp.MustCompile(
	`(?ms)\n?^[ \t]*` + regexp.QuoteMeta(pathHookOpenMarker) + `\n` +
		`.*?^[ \t]*` + regexp.QuoteMeta(pathHookCloseMarker) + `\n?`)

// pathHookPosixBody sources the generated fragment from any POSIX rc
// file. The fragment path is resolved at shell startup rather than
// baked in at install time, because these rc files sync to other
// machines. .zshenv runs on every zsh invocation including every
// script, so the body stays parameter expansion plus one stat.
var pathHookPosixBody = fmt.Sprintf(`__dfm_env="${XDG_CONFIG_HOME:-$HOME/.config}/dotfiles/%s"
[ -r "$__dfm_env" ] && . "$__dfm_env"
unset __dfm_env
`, pathFragmentPosixName)

// pathHookFishBody is the fish equivalent. fish has no ${VAR:-default},
// so the fallback needs its own `test -n`.
var pathHookFishBody = fmt.Sprintf(`set -l __dfm_base $XDG_CONFIG_HOME
test -n "$__dfm_base"; or set __dfm_base $HOME/.config
test -r $__dfm_base/dotfiles/%s; and source $__dfm_base/dotfiles/%s
set -e __dfm_base
`, pathFragmentFishName, pathFragmentFishName)

// renderPathHook returns the complete marker-delimited hook block,
// trailing newline included.
func renderPathHook(family string) string {
	body := pathHookPosixBody
	if family == "fish" {
		body = pathHookFishBody
	}
	return pathHookOpenMarker + "\n" + body + pathHookCloseMarker + "\n"
}

// installPathHook returns content with exactly one hook block present.
// An existing block is replaced in place so a body change in a newer
// dfm reaches rc files that already carry the hook.
func installPathHook(content []byte, family string) []byte {
	block := []byte(renderPathHook(family))
	if loc := pathHookBlockRe.FindIndex(content); loc != nil {
		out := make([]byte, 0, len(content)+len(block))
		out = append(out, content[:loc[0]]...)
		if content[loc[0]] == '\n' {
			out = append(out, '\n')
		}
		out = append(out, block...)
		return append(out, content[loc[1]:]...)
	}
	out := make([]byte, 0, len(content)+len(block)+1)
	out = append(out, content...)
	if len(content) > 0 {
		out = append(out, '\n')
	}
	return append(out, block...)
}

// removePathHook strips the hook block and the newline installPathHook
// wrote in front of it.
func removePathHook(content []byte) []byte {
	return pathHookBlockRe.ReplaceAll(content, nil)
}

// knownPathHookShell reports whether an explicitly requested shell is
// one pathHookFiles actually understands.
func knownPathHookShell(shell string) bool {
	switch shell {
	case "bash", "zsh", "fish", "sh", "profile":
		return true
	}
	return false
}

// pathHookFiles returns the rc files the hook belongs in for a shell,
// plus the fragment syntax family they source.
//
// zsh needs two: .zshenv covers every non-login invocation, but macOS
// /etc/zprofile runs path_helper for login shells, which rebuilds PATH
// and demotes whatever .zshenv prepended. Re-sourcing from .zprofile
// puts the managed dirs back in front.
func pathHookFiles(shell string) ([]string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("home dir: %w", err)
	}
	switch shell {
	case "zsh":
		return []string{
			filepath.Join(home, ".zshenv"),
			filepath.Join(home, ".zprofile"),
		}, "posix", nil
	case "bash":
		// Login bash reads the first of these that exists and stops, so
		// writing to a later one it shadows would install a hook bash
		// never sources.
		login := filepath.Join(home, ".profile")
		for _, name := range []string{".bash_profile", ".bash_login"} {
			if _, err := os.Stat(filepath.Join(home, name)); err == nil {
				login = filepath.Join(home, name)
				break
			}
		}
		return []string{filepath.Join(home, ".bashrc"), login}, "posix", nil
	case "fish":
		return []string{filepath.Join(home, ".config", "fish", "config.fish")}, "fish", nil
	default:
		return []string{filepath.Join(home, ".profile")}, "posix", nil
	}
}

func newPathHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Manage the one-time rc hook that sources the generated PATH fragment",
	}
	cmd.AddCommand(newPathHookInstallCmd(), newPathHookRemoveCmd())
	return cmd
}

func newPathHookInstallCmd() *cobra.Command {
	var (
		shellFlag string
		dryRun    bool
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write the PATH fragment and install the hook that sources it",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runPathHook(c, shellFlag, dryRun, force, true)
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing")
	cmd.Flags().BoolVar(&force, "force", false, "track an rc file even if it trips the secret scan")
	return cmd
}

func newPathHookRemoveCmd() *cobra.Command {
	var (
		shellFlag string
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove the dfm PATH hook from this shell's rc files",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runPathHook(c, shellFlag, dryRun, false, false)
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing")
	return cmd
}

// resolveHookShell applies the --shell flag or falls back to $SHELL.
// detectShell falls back to profile for anything unfamiliar; an explicit
// flag gets no such latitude, since a typo would create and write a
// startup file for the wrong shell.
func resolveHookShell(cmdName, shellFlag string) (string, error) {
	if shellFlag == "" {
		return detectShell(), nil
	}
	if !knownPathHookShell(shellFlag) {
		return "", exitf(exitResolveErr,
			"%s: unknown --shell %q (want bash, zsh, fish, sh or profile)", cmdName, shellFlag)
	}
	return shellFlag, nil
}

// runPathHook is the shared body of hook install and hook remove.
func runPathHook(c *cobra.Command, shellFlag string, dryRun, force, install bool) error {
	shell, err := resolveHookShell("path hook", shellFlag)
	if err != nil {
		return err
	}
	files, family, err := pathHookFiles(shell)
	if err != nil {
		return err
	}

	if install {
		if err := writePathFragment(c, shell, family, dryRun); err != nil {
			return err
		}
	}

	s, err := openStore(c.Context())
	if err != nil {
		return err
	}
	defer s.Close()

	for _, f := range files {
		if err := applyPathHookFile(c, s, f, family, dryRun, force, install); err != nil {
			return err
		}
	}
	return nil
}

// writePathFragment regenerates the sourceable fragment from the shell's
// rc file, which is safe only while that rc file still holds the managed
// blocks. Once use_fragment is on it no longer does, so regenerating
// would overwrite the user's whole PATH configuration with an empty
// fragment.
func writePathFragment(c *cobra.Command, shell, family string, dryRun bool) error {
	rc, err := rcFileForShell(shell)
	if err != nil {
		return err
	}
	dest := config.FragmentPath(pathFragmentName(family))
	if pathUseFragment(c.Context()) {
		fmt.Fprintf(c.OutOrStdout(), "fragment %s is the source of truth; not regenerating\n", dest)
		return nil
	}
	if dryRun {
		fmt.Fprintf(c.OutOrStdout(), "would write fragment %s (from %s)\n", dest, rc)
		return nil
	}

	data, err := os.ReadFile(rc)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", rc, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	body := renderPathFragment(family, findPathManagedEntries(data))
	if err := fsx.AtomicWrite(dest, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	fmt.Fprintf(c.OutOrStdout(), "wrote fragment %s\n", dest)
	return nil
}

// applyPathHookFile installs or removes the hook in one rc file,
// auto-tracking the file first so the edit goes through the normal
// snapshot-then-record path.
func applyPathHookFile(c *cobra.Command, s *store.Store, path, family string, dryRun, force, install bool) error {
	out := c.OutOrStdout()
	verb, past, action := "remove hook from", "removed hook from", "path.hook.remove"
	if install {
		verb, past, action = "install hook in", "installed hook in", "path.hook.install"
	}

	// Follow symlinks when the file exists so we edit the same bytes
	// the tracker hashes. Resolve also yields the ~-relative display
	// name; a file we are about to create has neither yet.
	target, display := path, path
	if canonical, d, err := tracker.Resolve(path); err == nil {
		target, display = canonical, d
	}

	current, readErr := os.ReadFile(target)
	exists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read %s: %w", target, readErr)
	}

	updated := removePathHook(current)
	if install {
		updated = installPathHook(current, family)
	}
	if exists && bytes.Equal(current, updated) {
		fmt.Fprintf(out, "no change: %s\n", display)
		return nil
	}
	if !exists && !install {
		return nil
	}

	ctx := c.Context()
	if dryRun {
		note := ""
		if _, _, err := resolveTracked(ctx, s, path); err != nil {
			note = " (would track)"
		}
		fmt.Fprintf(out, "would %s %s%s\n", verb, display, note)
		return nil
	}

	if !exists {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := fsx.AtomicWrite(target, nil, 0o644); err != nil {
			return fmt.Errorf("create %s: %w", target, err)
		}
	}

	file, err := writeTrackedEdit(c, s, "path hook", path, updated, action,
		map[string]any{"family": family}, force)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "%s %s\n", past, file.DisplayPath)
	return nil
}
