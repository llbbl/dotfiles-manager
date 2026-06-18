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
	"time"

	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/llbbl/dotfiles-manager/internal/fsx"
	"github.com/llbbl/dotfiles-manager/internal/secrets"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
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

// pathFishStubMsg is surfaced when the user explicitly targets fish via
// --shell=fish or a .fish file path. Fish rendering is its own task
// (dfm-mxf); until that lands, error cleanly rather than emitting
// malformed bash syntax into a fish config.
const pathFishStubMsg = "dfm path: fish shell support not implemented yet — see beads dfm-mxf"

// buildBashZshExportLine returns the per-iteration PATH assignment that
// goes inside the case fallthrough — prepending or appending the loop
// variable depending on direction. The double-quoted form survives word
// splitting on directories that contain spaces.
func buildBashZshExportLine(direction string) string {
	if direction == pathDirectionAppend {
		return `PATH="$PATH:$__dfm_d"`
	}
	return `PATH="$__dfm_d:$PATH"`
}

// renderPathBlockBashZsh emits the full managed entry — both markers
// and the for/case/done body — with a trailing newline after the close
// marker so callers can splice the result directly into a file.
//
// v1 constraint: dir tokens are well-formed shell words (no spaces, no
// ':', no shell metacharacters). The for-loop body emits them
// space-separated, unquoted; the marker comment emits them
// colon-separated regardless of shell (canonical per spec §5.1).
func renderPathBlockBashZsh(id string, updatedAt time.Time, direction string, dirs []string) string {
	var b strings.Builder
	b.WriteString(formatPathOpenMarker(id, updatedAt, direction, dirs))
	b.WriteByte('\n')
	fmt.Fprintf(&b, "for __dfm_d in %s; do\n", strings.Join(dirs, " "))
	b.WriteString("  case \":$PATH:\" in\n")
	b.WriteString("    *\":$__dfm_d:\"*) ;;\n")
	fmt.Fprintf(&b, "    *) %s ;;\n", buildBashZshExportLine(direction))
	b.WriteString("  esac\n")
	b.WriteString("done\n")
	b.WriteString("unset __dfm_d\n")
	b.WriteString("export PATH\n")
	b.WriteString(formatPathCloseMarker(id))
	b.WriteByte('\n')
	return b.String()
}

// isFishTarget reports whether the resolved target is for fish — either
// the user passed --shell=fish (yielding family "fish" from
// shellFamily) or --file pointed at a .fish file (also "fish"). Used
// by `dfm path add` to fail fast with the dfm-mxf hint.
func isFishTarget(family string) bool {
	return family == "fish"
}

func newPathAddCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		appendDir bool
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "add <dir>",
		Short: "Add a directory to the dfm-managed PATH entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			dir := args[0]
			if dir == "" {
				return exitf(exitResolveErr, "path add: <dir> must not be empty")
			}
			// Strict mutex: alias add tolerates both flags together; the
			// path-cmd spec (§4.1) deliberately diverges.
			if shellFlag != "" && fileFlag != "" {
				return exitf(exitResolveErr,
					"path add: --shell and --file are mutually exclusive")
			}

			target, family, err := resolveAliasTarget(shellFlag, fileFlag)
			if err != nil {
				return err
			}
			if isFishTarget(family) {
				return exitf(exitResolveErr, pathFishStubMsg)
			}

			direction := pathDirectionPrepend
			if appendDir {
				direction = pathDirectionAppend
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

			// Locate every managed entry and partition by direction.
			entries := findPathManagedEntries(current)
			var matching []PathManagedEntry
			for _, e := range entries {
				if e.Marker.Direction == direction {
					matching = append(matching, e)
				}
			}
			// Corruption guard (§5.4 / §7 case #19): more than one
			// managed entry for the same direction means a manual edit
			// went wrong. Refuse to auto-merge.
			if len(matching) > 1 {
				return exitf(exitResolveErr,
					"multiple dfm-managed %s entries found in %s; please inspect and clean up manually",
					direction, file.DisplayPath)
			}

			var (
				existing       *PathManagedEntry
				existingDirs   []string
				markerIDOld    string
			)
			if len(matching) == 1 {
				existing = &matching[0]
				existingDirs = append(existingDirs, existing.Marker.Dirs...)
				markerIDOld = existing.Marker.ID
			}

			// Dedup against existing dirs in this direction using
			// normalized comparison; the user's literal token is what
			// gets persisted.
			needle := normalizePathDir(dir)
			for _, d := range existingDirs {
				if normalizePathDir(d) == needle {
					return exitf(exitAlreadyOrMiss,
						"%q already managed in %s entry of %s",
						dir, direction, file.DisplayPath)
				}
			}

			newDirs := append([]string{}, existingDirs...)
			newDirs = append(newDirs, dir)

			// Secret scan on the dir token. Mirror alias add's UX: print
			// a tab-separated table on stderr, exit exitSecretsErr
			// unless --force is set. --force does NOT bypass dedup.
			res, scanErr := secrets.ScanReader(bytes.NewReader([]byte(dir)))
			if scanErr != nil {
				return fmt.Errorf("secret scan: %w", scanErr)
			}
			secretFlagged := !res.Skipped && len(res.Findings) > 0
			if secretFlagged {
				if !force {
					tw := tabwriter.NewWriter(c.ErrOrStderr(), 0, 0, 2, ' ', 0)
					fmt.Fprintln(tw, "RULE\tLINE\tEXCERPT")
					for _, fi := range res.Findings {
						fmt.Fprintf(tw, "%s\t%d\t%s\n", fi.Rule, fi.Line, fi.Excerpt)
					}
					tw.Flush()
					fmt.Fprintln(c.ErrOrStderr(),
						"path add aborted: secrets detected (--force to override)")
					os.Exit(exitSecretsErr)
				}
				fmt.Fprintf(c.ErrOrStderr(),
					"warning: %d secret finding(s) in dir token; proceeding due to --force\n",
					len(res.Findings))
			}

			// Snapshot first, mirroring alias add. ReasonPreEdit comes
			// from TakePreEdit — matches the repo convention for
			// mutation commands.
			mgr, mgrErr := newSnapshotManager(c.Context(), s)
			if mgrErr != nil {
				return fmt.Errorf("snapshot manager: %w", mgrErr)
			}
			snap, err := snapshot.TakePreEdit(c.Context(), mgr, canonical, file)
			if err != nil {
				return err
			}

			newID := pathMarkerID(direction, newDirs)
			block := renderPathBlockBashZsh(newID, time.Now().UTC(), direction, newDirs)

			var newContent []byte
			if existing != nil {
				// In-place splice: replace the old block bytes with the
				// new block. BlockEnd already includes the trailing
				// newline if there was one — renderPathBlockBashZsh
				// always emits a trailing newline, so byte semantics
				// line up.
				newContent = make([]byte, 0, len(current)-(existing.BlockEnd-existing.BlockStart)+len(block))
				newContent = append(newContent, current[:existing.BlockStart]...)
				newContent = append(newContent, block...)
				newContent = append(newContent, current[existing.BlockEnd:]...)
			} else {
				// Append at EOF. Ensure exactly one blank line of
				// breathing room before the block when the file has
				// existing content — readability matches what installer
				// snippets do.
				newContent = make([]byte, 0, len(current)+len(block)+2)
				newContent = append(newContent, current...)
				if len(current) > 0 {
					if !bytes.HasSuffix(current, []byte("\n")) {
						newContent = append(newContent, '\n')
					}
					if !bytes.HasSuffix(newContent, []byte("\n\n")) {
						newContent = append(newContent, '\n')
					}
				}
				newContent = append(newContent, block...)
			}

			if err := fsx.AtomicWrite(canonical, newContent, info.Mode().Perm()); err != nil {
				return fmt.Errorf("write %s: %w", canonical, err)
			}

			sum := sha256.Sum256(newContent)
			newHash := hex.EncodeToString(sum[:])

			subAction := "add"
			if force && secretFlagged {
				subAction = "force-add"
			}

			// Privacy note: alias add deliberately omits the alias body
			// from audit. The dir token is the audited unit here; it's
			// already public-facing (it lives in PATH at runtime), so
			// recording it lets audit consumers reconstruct the
			// transition without re-reading the rc file.
			if err := tracker.RecordHashChange(c.Context(), s, file, newHash, snap.ID, "path.add", map[string]any{
				"dir":             dir,
				"direction":       direction,
				"marker_id_new":   newID,
				"marker_id_old":   markerIDOld,
				"snapshot_id":     snap.ID,
				"sub_action":      subAction,
			}); err != nil {
				return err
			}

			fmt.Fprintf(c.OutOrStdout(),
				"added %q to %s entry in %s\n", dir, direction, file.DisplayPath)
			return nil
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
