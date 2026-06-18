package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/spf13/cobra"
)

// pathDirectionPrepend / pathDirectionAppend are the canonical direction
// tokens emitted into the `direction=` field of every dfm:path marker.
// They are the source of truth — both rendering and parsing read from
// these constants so the comment shape can never drift.
const (
	pathDirectionPrepend = "prepend"
	pathDirectionAppend  = "append"
)

// pathMarkerID returns the 8-hex-char id derived from the (direction,
// dirs) tuple. The hash function is sha256, truncated to the first 8
// hex chars (32 bits) — collision risk inside a single rc file is
// astronomically low and the shorter id keeps marker comments scannable.
//
// The id is deterministic: identical input always produces an identical
// id. The dirs slice is joined with ":" — the same separator the
// `dirs=` marker field uses — so the hash input mirrors the on-disk
// canonical form. Direction is prefixed with "<direction>:" so the same
// dirs in prepend vs append direction produce distinct ids.
func pathMarkerID(direction string, dirs []string) string {
	sum := sha256.Sum256([]byte(direction + ":" + strings.Join(dirs, ":")))
	return hex.EncodeToString(sum[:])[:8]
}

// PathMarkerLine is the parsed shape of an opening dfm:path marker
// comment. Raw retains the original line text (without the trailing
// newline) so callers that want to re-emit a marker verbatim can do so
// without re-formatting and risking byte drift.
type PathMarkerLine struct {
	ID        string
	UpdatedAt time.Time
	Direction string
	Dirs      []string
	Raw       string
}

// PathManagedEntry is one paired-marker block found in an rc file. The
// byte ranges are zero-based offsets into the original file bytes. They
// are intentionally byte-precise (not line-based) so callers can splice
// content with `content[:BlockStart] + new + content[BlockEnd:]` without
// having to re-walk the file.
//
// OpenStart..OpenEnd covers the opening marker line, NOT including the
// trailing newline. CloseStart..CloseEnd covers the closing marker
// line, also NOT including the trailing newline. BlockStart equals
// OpenStart; BlockEnd points one byte past the closing marker's
// trailing newline if there is one, otherwise equals CloseEnd. That
// asymmetry is what makes splice operations "delete the whole block,
// including its trailing newline" idiomatic.
type PathManagedEntry struct {
	Marker     PathMarkerLine
	OpenStart  int
	OpenEnd    int
	CloseStart int
	CloseEnd   int
	BlockStart int
	BlockEnd   int
}

// formatPathOpenMarker renders the opening marker comment line per
// spec §5.1. The "  " double-space separator between metadata fields
// matches the installer-norm convention (e.g. mise / nvm) and makes
// the line trivially grep-friendly.
//
// No trailing newline is emitted; callers add it if they're splicing
// into a file.
func formatPathOpenMarker(id string, updatedAt time.Time, direction string, dirs []string) string {
	return fmt.Sprintf(
		"# dfm:path:%s >>>  updated=%s  direction=%s  dirs=%s",
		id,
		updatedAt.UTC().Format(time.RFC3339),
		direction,
		strings.Join(dirs, ":"),
	)
}

// formatPathCloseMarker renders the closing marker comment line. The
// id-only shape keeps block bounds unambiguous without re-parsing
// shell-specific terminators (`done`, `end`, etc.).
func formatPathCloseMarker(id string) string {
	return fmt.Sprintf("# dfm:path:%s <<<", id)
}

// pathOpenMarkerRe matches an opening marker line and captures the
// four fields (id, updated, direction, dirs). The line is anchored at
// both ends; leading/trailing whitespace is tolerated to keep us
// resilient to manual edits that re-indent the comment. The `dirs=`
// capture is greedy to the end-of-line so colons inside individual
// dir tokens (which are legal in POSIX paths and are exactly what the
// `:` separator delimits at the dirs-list level — there is no escape
// for a literal `:` inside a dir token in v1) round-trip cleanly.
var pathOpenMarkerRe = regexp.MustCompile(
	`^\s*# dfm:path:([0-9a-f]{8}) >>>\s+updated=(\S+)\s+direction=(\S+)\s+dirs=(.*?)\s*$`,
)

// pathCloseMarkerRe matches a closing marker line and captures the id.
var pathCloseMarkerRe = regexp.MustCompile(
	`^\s*# dfm:path:([0-9a-f]{8}) <<<\s*$`,
)

// parsePathOpenMarker parses an opening marker line into a
// PathMarkerLine. The second return is false if the line doesn't look
// like a dfm:path opening marker — callers use this as a cheap "is
// this our line?" predicate. Empty `dirs=` is legal and decodes to a
// nil/empty Dirs slice.
func parsePathOpenMarker(line string) (PathMarkerLine, bool) {
	// Strip a trailing CRLF or LF so the regex's `$` matches whether
	// the caller fed us the line with or without its terminator.
	line = strings.TrimRight(line, "\r\n")
	m := pathOpenMarkerRe.FindStringSubmatch(line)
	if m == nil {
		return PathMarkerLine{}, false
	}
	updated, err := time.Parse(time.RFC3339, m[2])
	if err != nil {
		return PathMarkerLine{}, false
	}
	var dirs []string
	if m[4] != "" {
		dirs = strings.Split(m[4], ":")
	}
	return PathMarkerLine{
		ID:        m[1],
		UpdatedAt: updated.UTC(),
		Direction: m[3],
		Dirs:      dirs,
		Raw:       line,
	}, true
}

// parsePathCloseMarker parses a closing marker line and returns its
// id. The second return is false if the line isn't a dfm:path closing
// marker.
func parsePathCloseMarker(line string) (string, bool) {
	line = strings.TrimRight(line, "\r\n")
	m := pathCloseMarkerRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// normalizePathDir applies the dedup-equivalence rules:
//
//   - strip a trailing slash (`/foo/` == `/foo`), but never strip the
//     last slash from "/" — that would yield an empty string.
//   - collapse `~` and `$HOME` to the actual home dir, so the three
//     spellings `~/foo`, `$HOME/foo`, and `/Users/me/foo` all compare
//     equal when HOME=/Users/me.
//
// This helper is COMPARISON-ONLY. The caller's literal token (whatever
// they typed) is what gets written into the file. Normalization output
// must NEVER be persisted, or `dfm path list` would show user-typed
// tokens that don't match their input.
func normalizePathDir(dir string) string {
	// $HOME expansion — only the leading "$HOME/" or bare "$HOME".
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		switch {
		case dir == "$HOME":
			dir = home
		case strings.HasPrefix(dir, "$HOME/"):
			dir = filepath.Join(home, strings.TrimPrefix(dir, "$HOME/"))
		case dir == "~":
			dir = home
		case strings.HasPrefix(dir, "~/"):
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~/"))
		}
	}
	// Trailing slash strip, but preserve root.
	for len(dir) > 1 && strings.HasSuffix(dir, "/") {
		dir = strings.TrimSuffix(dir, "/")
	}
	return dir
}

// findPathManagedEntries scans content for paired dfm:path markers
// and returns one PathManagedEntry per well-formed block. Pairing
// rule: an opening marker is paired with the NEXT closing marker
// carrying the same id, on a strictly later line. Anything that
// doesn't pair — opens without a matching close, closes without a
// preceding open, mismatched ids — is dropped (logged at debug).
//
// The helper is shell-agnostic by design: it never looks at the
// for-loop body between the markers. That keeps the skeleton
// decoupled from bash/zsh vs fish block bodies, which land in
// follow-up tasks (dfm-5w3, dfm-mxf).
func findPathManagedEntries(content []byte) []PathManagedEntry {
	type openCandidate struct {
		marker     PathMarkerLine
		start, end int
	}
	var opens []openCandidate
	type closeCandidate struct {
		id         string
		start, end int
	}
	var closes []closeCandidate

	// One pass: walk every line, recording its byte range and
	// classifying it as open, close, or neither.
	i := 0
	for i < len(content) {
		// Find the end of this line (LF, CR, or EOF).
		j := i
		for j < len(content) && content[j] != '\n' {
			j++
		}
		// lineStart..lineEnd is the line itself, excluding the \n.
		line := string(content[i:j])
		if marker, ok := parsePathOpenMarker(line); ok {
			opens = append(opens, openCandidate{marker: marker, start: i, end: j})
		} else if id, ok := parsePathCloseMarker(line); ok {
			closes = append(closes, closeCandidate{id: id, start: i, end: j})
		}
		// Advance past this line and its terminator.
		if j < len(content) {
			i = j + 1
		} else {
			i = j
		}
	}

	if len(opens) == 0 {
		return nil
	}

	out := make([]PathManagedEntry, 0, len(opens))
	usedClose := make([]bool, len(closes))
	for _, op := range opens {
		// Find the next unused close with the same id that starts
		// strictly after this open.
		matchedIdx := -1
		for k, cl := range closes {
			if usedClose[k] {
				continue
			}
			if cl.start <= op.end {
				continue
			}
			if cl.id != op.marker.ID {
				continue
			}
			matchedIdx = k
			break
		}
		if matchedIdx < 0 {
			// Unpaired open. Best-effort debug log; never panic.
			dlog.Discard.Debug("dfm:path open marker without matching close",
				"id", op.marker.ID)
			continue
		}
		cl := closes[matchedIdx]
		usedClose[matchedIdx] = true

		// BlockEnd: one past the closing marker's trailing newline,
		// if there is one. Otherwise equals cl.end.
		blockEnd := cl.end
		if blockEnd < len(content) && content[blockEnd] == '\n' {
			blockEnd++
		}
		out = append(out, PathManagedEntry{
			Marker:     op.marker,
			OpenStart:  op.start,
			OpenEnd:    op.end,
			CloseStart: cl.start,
			CloseEnd:   cl.end,
			BlockStart: op.start,
			BlockEnd:   blockEnd,
		})
	}
	// Closes with no matching open are also noted at debug.
	for k, cl := range closes {
		if !usedClose[k] {
			dlog.Discard.Debug("dfm:path close marker without matching open",
				"id", cl.id)
		}
	}
	return out
}

// newPathCmd builds the `dfm path` command group: a parent with three
// stub subcommands. Real business logic lives in follow-up tasks; the
// stubs exist so the CLI surface is reachable from day one.
func newPathCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Manage PATH entries in tracked rc files (coalesced, idempotent)",
	}
	cmd.AddCommand(newPathAddCmd(), newPathRemoveCmd(), newPathListCmd())
	return cmd
}

// pathStubMsg is the error returned by every stub subcommand. The
// message names the beads issue tracking the real implementation so
// users (and future maintainers) know where the work lives.
const pathStubMsg = "dfm path %s: not implemented yet — see beads dfm-5w3 / dfm-mxf / dfm-2bl / dfm-5mq"

func newPathAddCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		appendDir bool
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "add <dir>",
		Short: "Add a directory to the dfm-managed PATH entry (stub)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return exitf(exitResolveErr, pathStubMsg, "add")
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	cmd.Flags().BoolVar(&appendDir, "append", false, "write into the append-direction entry (default: prepend)")
	cmd.Flags().BoolVar(&force, "force", false, "bypass secret scan (does NOT bypass dedup)")
	return cmd
}

func newPathRemoveCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
	)
	cmd := &cobra.Command{
		Use:   "remove <dir>",
		Short: "Remove a directory from the dfm-managed PATH entry (stub)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return exitf(exitResolveErr, pathStubMsg, "remove")
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	return cmd
}

func newPathListCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List dfm-managed PATH entries (stub)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return exitf(exitResolveErr, pathStubMsg, "list")
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|fish|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of tab-separated rows")
	return cmd
}
