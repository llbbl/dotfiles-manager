// Package shellrc detects the user's shell and resolves the rc file a
// command should edit.
package shellrc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Detect returns the basename of $SHELL, lowercased. Empty if unset.
func Detect() string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(sh))
}

// RCFile maps a shell name to its conventional rc file. Anything
// unrecognised falls back to ~/.profile (POSIX-compatible).
func RCFile(shell string) (string, error) {
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

// Family collapses a shell name to "fish" or "posix" for alias quoting
// and parsing. Alias syntax is identical in bash and zsh, so zsh is posix
// here; PATH rendering uses PathFamily instead.
func Family(shell string) string {
	if shell == "fish" {
		return "fish"
	}
	return "posix"
}

// PathFamily maps a shell name to a PATH rendering family. Unlike Family,
// zsh stays separate: its native `path` array promotes and dedupes in a
// way the POSIX body cannot.
func PathFamily(shell string) string {
	switch shell {
	case "fish":
		return "fish"
	case "zsh":
		return "zsh"
	default:
		return "posix"
	}
}

// FromFilename guesses the shell behind an rc file. Zsh owns the ".z*"
// family plus anything spelling out "zsh"; everything unrecognised is sh.
func FromFilename(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(base, ".fish"):
		return "fish"
	case strings.Contains(base, "zsh"), strings.HasPrefix(base, ".z"):
		return "zsh"
	default:
		return "sh"
	}
}

// ResolveTarget picks the file to edit from optional --shell / --file
// overrides plus $SHELL. It returns the absolute path and the raw shell
// name, which callers collapse to the family their syntax needs.
func ResolveTarget(shellFlag, fileFlag string) (string, string, error) {
	if fileFlag != "" {
		abs, err := filepath.Abs(fileFlag)
		if err != nil {
			return "", "", fmt.Errorf("resolve --file: %w", err)
		}
		shell := shellFlag
		if shell == "" {
			shell = FromFilename(abs)
		}
		return abs, shell, nil
	}
	shell := shellFlag
	if shell == "" {
		shell = Detect()
	}
	path, err := RCFile(shell)
	if err != nil {
		return "", "", err
	}
	return path, shell, nil
}
