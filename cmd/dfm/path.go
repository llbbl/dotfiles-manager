package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newPathCmd builds the parent `dfm path` command and registers its
// add/remove/list subcommands. The family mirrors `dfm alias`: a
// shell-aware, snapshot-backed editor for tracked rc files. This
// commit lands the skeleton + shared helpers only — the add/remove/
// list business logic is implemented in follow-up beads tasks
// (dfm-5w3, dfm-mxf, dfm-2bl, dfm-5mq).
func newPathCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Manage PATH entries in tracked rc files",
	}
	cmd.AddCommand(newPathAddCmd(), newPathRemoveCmd(), newPathListCmd())
	return cmd
}

// pathNotImplementedMsg is the user-facing message every stub returns
// until the follow-up beads tasks land. We exit with exitResolveErr
// rather than exitOK so scripts can't accidentally treat the stub as a
// success.
const pathNotImplementedMsg = "dfm path %s: not implemented yet — see beads dfm-5w3 (add), dfm-mxf (remove), dfm-2bl (list), dfm-5mq (acceptance)"

func newPathAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <dir>",
		Short: "Snapshot then prepend a PATH entry to a tracked rc file",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, _ []string) error {
			fmt.Fprintf(c.ErrOrStderr(), pathNotImplementedMsg+"\n", "add")
			os.Exit(exitResolveErr)
			return nil
		},
	}
}

func newPathRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <dir>",
		Short: "Remove every dfm-managed PATH entry for <dir> from a tracked rc file",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, _ []string) error {
			fmt.Fprintf(c.ErrOrStderr(), pathNotImplementedMsg+"\n", "remove")
			os.Exit(exitResolveErr)
			return nil
		},
	}
}

func newPathListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List PATH entries parsed from a tracked rc file (best-effort)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			fmt.Fprintf(c.ErrOrStderr(), pathNotImplementedMsg+"\n", "list")
			os.Exit(exitResolveErr)
			return nil
		},
	}
}

// --- shared helpers used by the add/remove/list follow-ups ---

// pathMarkerID derives a stable 8-character hex ID from a directory
// string. We use sha256[:8] (Q1 in REQUIREMENTS §8): deterministic per
// input, dedup-friendly, and survives store rebuilds.
//
// Note: the ID is derived from the literal `dir` token the caller
// passed, NOT from a normalized form. That means `~/foo` and
// `$HOME/foo` produce different IDs. Dedup at write time goes through
// normalizePathDir; the marker ID is purely a stable handle for
// removal.
func pathMarkerID(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:])[:8]
}

// pathMarkerTimeFormat is the ISO 8601 / RFC 3339 stamp used inside
// the marker comment. We pin to seconds-precision UTC so the marker
// stays a single readable token and round-trips cleanly through the
// parser regex below.
const pathMarkerTimeFormat = "2006-01-02T15:04:05Z07:00"

// formatPathMarker renders the marker comment line that prefixes every
// dfm-managed PATH block (and trails every line-mode entry). Shape
// (REQUIREMENTS §5.1):
//
//	# dfm:path:<id>  added=<ISO8601>  dir=<dir>
//
// The two-space gaps between fields are load-bearing — they're what
// parsePathMarker uses to split fields without having to QuoteMeta the
// dir (which can contain `=` and spaces).
func formatPathMarker(id, dir string, addedAt time.Time) string {
	return fmt.Sprintf("# dfm:path:%s  added=%s  dir=%s",
		id, addedAt.UTC().Format(pathMarkerTimeFormat), dir)
}

// pathMarkerRe matches the marker comment line produced by
// formatPathMarker. We accept the leading `#` with optional surrounding
// whitespace, capture the 8-char hex ID, then walk the `added=` and
// `dir=` fields. The `dir` capture is greedy-to-EOL so directories
// containing `=` survive intact; trailing whitespace is trimmed by the
// parser.
var pathMarkerRe = regexp.MustCompile(
	`^[ \t]*#[ \t]*dfm:path:([0-9a-f]{8})[ \t]+added=([^ \t]+)[ \t]+dir=(.+?)[ \t]*$`)

// parsePathMarker is the inverse of formatPathMarker. Returns
// (id, dir, true) on a clean match; (_, _, false) for any line that
// doesn't carry the marker. The added-at stamp is intentionally not
// returned — callers that need it can re-parse the captured group, but
// nothing in remove/list currently does.
func parsePathMarker(line string) (id, dir string, ok bool) {
	m := pathMarkerRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimRight(m[3], " \t"), true
}

// normalizePathDir produces the canonical form used for "is this dir
// already on PATH?" dedup comparisons ONLY. It is NEVER used to
// rewrite the user's literal before writing to disk — REQUIREMENTS §3
// explicitly bans silent canonicalization.
//
// Rules:
//   - Trim trailing slash (but not the root `/` itself).
//   - Replace a leading `~/` with `$HOME/`.
//   - If `$HOME` is set, replace a leading `$HOME/` with the absolute
//     home directory. This collapses `~/foo`, `$HOME/foo`, and
//     `/Users/me/foo` to one form for comparison.
//
// The function is best-effort: if `$HOME` is unset we leave the
// `$HOME/` prefix in place and the caller will simply not dedup
// against the absolute form. That's the right failure mode — we'd
// rather over-add than silently rewrite.
func normalizePathDir(dir string) string {
	if dir == "" {
		return ""
	}
	// Trailing slash strip — but only if there's something else before
	// it, otherwise `/` itself would become "".
	for len(dir) > 1 && strings.HasSuffix(dir, "/") {
		dir = dir[:len(dir)-1]
	}
	// `~/foo` → `$HOME/foo`. We intentionally do NOT handle bare `~`
	// (without trailing slash) or `~user/` forms; those are uncommon
	// in rc files and the cost of getting them wrong is silent
	// rewriting.
	if strings.HasPrefix(dir, "~/") {
		dir = "$HOME/" + dir[2:]
	}
	// `$HOME/foo` → `<abs-home>/foo` when $HOME is set. Match the
	// literal `$HOME` token; don't go through os.ExpandEnv (which
	// would also expand `$HOMEBREW_PREFIX` etc).
	if home := os.Getenv("HOME"); home != "" {
		if dir == "$HOME" {
			return home
		}
		if strings.HasPrefix(dir, "$HOME/") {
			dir = home + "/" + dir[len("$HOME/"):]
		}
	}
	return dir
}
