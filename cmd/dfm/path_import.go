package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/fsx"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/spf13/cobra"
)

// path_import.go implements the read-only `dfm path import` subcommand:
// walk a tracked rc file, classify every PATH-touching line, and emit
// either a human-readable report or a JSON document describing what we
// would consolidate into a single dfm-managed prepend entry. Mutation is
// explicitly out of scope for this PR — running without --dry-run exits
// exitResolveErr. The apply flow lands in a follow-up.
//
// Design notes:
//   - Parsing is line-oriented and shell-substring driven, NOT a full
//     shell parser. We recognize a small set of well-known shapes
//     (bare prepend / pnpm-style guarded block / dynamic activations)
//     and bucket anything else as UNKNOWN. We do NOT evaluate shell.
//   - The managed-block detector reuses findPathManagedEntries from
//     path.go so the marker format stays defined in exactly one place.
//   - Dir literal resolution computes a normalized form for ordering
//     and dedup-detection only; the user's original literal is what
//     ends up in the proposal so re-rendering is byte-faithful.

// pathImportShape distinguishes the two IMPORTABLE flavors so the JSON
// consumer can render them differently. We intentionally don't carry a
// `class` tag on entries — each bucket is its own field on
// pathImportReport, so the bucket IS the classification.
type pathImportShape string

const (
	pathImportShapeBare    pathImportShape = "bare"
	pathImportShapeGuarded pathImportShape = "guarded"
)

// pathImportEntry is one classified line (or block, for guarded /
// managed) in the import report.
type pathImportEntry struct {
	Line int             `json:"line"`
	// EndLine is the 1-based last line index of this entry. For single-
	// line entries (bare IMPORTABLE, dynamic, unknown) it equals Line;
	// for guarded blocks it points at the `esac` line so apply can splice
	// the entire block by line range. We keep it off the JSON schema —
	// consumers can derive multi-line bounds by parsing `raw`.
	EndLine int `json:"-"`
	Raw  string          `json:"raw"`
	Dir  string          `json:"dir,omitempty"`
	// Direction is "prepend" for v1 IMPORTABLE entries; empty otherwise.
	// We keep the field on the struct so the JSON shape stays uniform.
	Direction string          `json:"direction,omitempty"`
	Shape     pathImportShape `json:"shape,omitempty"`

	// Reason carries the short hint for SKIP entries (dynamic/unknown).
	Reason string `json:"reason,omitempty"`

	// Managed-block metadata: only populated for "skipped_managed" entries.
	MarkerID  string `json:"marker_id,omitempty"`
	DirCount  int    `json:"dir_count,omitempty"`

	// dupOf is non-zero when this IMPORTABLE entry resolves to the same
	// normalized dir as a previous IMPORTABLE entry. It points at that
	// earlier entry's line number. We emit it as a sub-note on the human
	// output and leave it off the JSON to keep the schema flat — JSON
	// consumers can re-derive duplicates from the (dir, direction) tuple.
	dupOf int
}

// pathImportReport is the structured outcome of a single import scan.
type pathImportReport struct {
	File           string            `json:"file"`
	Importable     []pathImportEntry `json:"importable"`
	SkippedDynamic []pathImportEntry `json:"skipped_dynamic"`
	SkippedManaged []pathImportEntry `json:"skipped_managed"`
	SkippedUnknown []pathImportEntry `json:"skipped_unknown"`
	Proposal       *pathImportProposal `json:"proposal,omitempty"`
}

// pathImportProposal is the would-be consolidated managed entry. v1
// always proposes direction=prepend; the user can split into a separate
// append entry after the apply flow lands.
type pathImportProposal struct {
	Direction string   `json:"direction"`
	Dirs      []string `json:"dirs"`
}

// --- regex shapes (line-oriented; intentionally conservative) -------

// pathImportLiteralBareRe matches the four IMPORTABLE literal shapes:
//   - quoted absolute path:        "/foo/bar"
//   - quoted $HOME / ${HOME}:      "$HOME/foo" or "${HOME}/foo"
//   - quoted ~:                    "~/foo"
//   - bare $HOME outside quotes:   $HOME/foo
//
// Two top-level alternation arms; only one captures per match:
//   - group 1: the literal inside double quotes (the whole token
//     before the trailing `:$PATH` that lives inside the quotes).
//   - group 2: the bare-$HOME literal when quotes are absent.
//
// The character class deliberately accepts `$`, `{`, `}`, `-`, `.`, and
// `_` so `$HOME` / `${HOME}` / hyphenated dir names all round-trip. We
// do NOT accept `(`, backtick, or whitespace inside the literal so any
// command substitution shape falls through to UNKNOWN.
var pathImportLiteralBareRe = regexp.MustCompile(
	`^(?:export\s+)?PATH\s*=\s*` +
		`(?:` +
		`"((?:\$\{?HOME\}?|~)?/[A-Za-z0-9._\-/${}]+):\$PATH"` +
		`|` +
		`(\$HOME/[A-Za-z0-9._/-]+):\$PATH` +
		`)` +
		`\s*(?:;\s*export\s+PATH\s*)?$`,
)

// pathImportTouchesPATH is the cheap predicate for "does this line
// mention PATH at all?". We use it as the catch-all so anything that
// touches PATH without matching a known shape lands in SKIPPED-unknown.
var pathImportTouchesPATH = regexp.MustCompile(
	`(?:^|[^A-Za-z_])PATH(?:=|\s|:|$)`,
)

// pathImportEvalRe recognizes `eval "$(...)"` and `eval $(...)`. The
// arguments inside the substitution don't matter — we never evaluate.
var pathImportEvalRe = regexp.MustCompile(
	`^\s*eval\s+["']?\$\(`,
)

// pathImportSourceRe recognizes `source <path>` and `. <path>`. The
// guarded forms (`[ -f X ] && source X`, `[[ -f X ]] && source X`,
// `command -v X >/dev/null && eval ...`) get caught here too via the
// substring scan in classifyDynamic — this regex is just the simple
// case at the start of the line.
var pathImportSourceRe = regexp.MustCompile(
	`^\s*(?:source|\.)\s+\S+`,
)

// pathImportGuardedExportRe matches the export-line inside a pnpm-style
// guarded block:    *) export PATH="<literal>:$PATH" ;;
// Captures the literal token in group 1. Same character-class rules as
// pathImportLiteralBareRe so `$HOME` / `${HOME}` / `~` work uniformly.
var pathImportGuardedExportRe = regexp.MustCompile(
	`\*\)\s*(?:export\s+)?PATH\s*=\s*"((?:\$\{?HOME\}?|~)?/?[A-Za-z0-9._\-/${}]+):\$PATH"\s*;;`,
)

// pathImportGuardedCaseRe matches the case-guard line:
//     *":<literal>:"*) ;;
// Captures the literal token in group 1.
var pathImportGuardedCaseRe = regexp.MustCompile(
	`\*":((?:\$\{?HOME\}?|~)?/?[A-Za-z0-9._\-/${}]+):"\*\)\s*;;`,
)

// pluginLoaderNames are the substrings we use to identify plugin-manager
// loader lines that should be skipped under "plugin-loader". They're
// matched case-insensitively. Order doesn't matter; we scan all.
var pluginLoaderNames = []string{
	"oh-my-zsh.sh",
	"zinit",
	"antigen",
	"znap",
	"sheldon",
}

// --- helpers --------------------------------------------------------

// stripComment removes a trailing `#…` comment from the line when it's
// not inside quotes. Cheap and dumb: it only treats `#` as a comment
// when it's preceded by whitespace (or at start of line). Enough to
// keep us from misclassifying things like `export PATH=... # note`.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return strings.TrimRight(line[:i], " \t")
		}
	}
	return line
}

// classifyDynamic returns ("", "") if `line` is not a dynamic shape, or
// (reason, raw) when it is. The raw is the original line bytes; reason
// is one of "eval-activate", "source", "plugin-loader".
func classifyDynamic(line string) string {
	t := strings.TrimSpace(line)
	if t == "" {
		return ""
	}
	// Plugin-loader takes priority: it's the most specific.
	lower := strings.ToLower(t)
	for _, n := range pluginLoaderNames {
		if strings.Contains(lower, n) {
			// Only count it as a plugin-loader skip if the line ALSO
			// looks like it's loading code (source/eval/`.`) or is a
			// bare assignment of the loader path. Otherwise something
			// like a comment mentioning oh-my-zsh would be flagged.
			if pathImportEvalRe.MatchString(t) ||
				pathImportSourceRe.MatchString(t) ||
				strings.Contains(t, "source ") ||
				strings.Contains(t, "source\t") {
				return "plugin-loader"
			}
		}
	}
	if pathImportEvalRe.MatchString(t) {
		return "eval-activate"
	}
	if pathImportSourceRe.MatchString(t) {
		return "source"
	}
	// Guarded sources: `[[ -f X ]] && source X` or `[ -f X ] && . X`.
	if strings.Contains(t, "&&") &&
		(strings.Contains(t, " source ") || strings.Contains(t, " . ")) {
		return "source"
	}
	return ""
}

// classifyImportableBare returns (dir, true) when `line` is a bare
// IMPORTABLE prepend. The returned dir is the literal token (quotes
// stripped) — never normalized.
func classifyImportableBare(line string) (string, bool) {
	t := strings.TrimSpace(stripComment(line))
	if t == "" {
		return "", false
	}
	// Strip a trailing `; export PATH` if present so the regex doesn't
	// have to spell every whitespace variant.
	m := pathImportLiteralBareRe.FindStringSubmatch(t)
	if m == nil {
		return "", false
	}
	// Group 1: quoted literal; group 2: bare $HOME literal.
	if m[1] != "" {
		return m[1], true
	}
	if m[2] != "" {
		return m[2], true
	}
	return "", false
}

// extractGuardedLiteral pulls the literal off a case-guard or
// export-line within a pnpm-style block. Returns ("", false) if neither
// shape matches.
func extractGuardedLiteral(line string) (string, bool) {
	if m := pathImportGuardedCaseRe.FindStringSubmatch(line); m != nil {
		return m[1], true
	}
	if m := pathImportGuardedExportRe.FindStringSubmatch(line); m != nil {
		return m[1], true
	}
	return "", false
}

// classifiedGuardedBlock represents a successfully-recognized guarded
// IMPORTABLE block. StartLine is the 1-based line number of the
// opening `case` keyword. Raw is the joined block text (no trailing
// newline). Dir is the literal we'll propose.
type classifiedGuardedBlock struct {
	StartLine int
	EndLine   int
	Raw       string
	Dir       string
}

// tryParseGuardedBlock examines `lines` starting at index `i` for a
// pnpm-style block:
//
//   case ":$PATH:" in
//     *":<literal>:"*) ;;
//     *) export PATH="<literal>:$PATH" ;;
//   esac
//
// On success returns the parsed block and the number of lines it
// consumed (>=4). On failure returns (nil, 0).
//
// We're lenient about whitespace and the order of the two case arms,
// but the literal in the guard MUST match the literal in the export
// (modulo whitespace) — mismatch falls through to UNKNOWN per spec.
func tryParseGuardedBlock(lines []string, i int) (*classifiedGuardedBlock, int) {
	if i >= len(lines) {
		return nil, 0
	}
	head := strings.TrimSpace(lines[i])
	if !strings.HasPrefix(head, "case ") || !strings.Contains(head, `":$PATH:"`) || !strings.HasSuffix(head, " in") {
		return nil, 0
	}

	// Look ahead up to 6 lines for the matching `esac`. Each arm
	// can be on its own line; we accumulate the literals we find.
	var guardLit, exportLit string
	end := -1
	maxLookahead := 6
	for j := i + 1; j < len(lines) && j <= i+maxLookahead; j++ {
		t := strings.TrimSpace(lines[j])
		if t == "esac" {
			end = j
			break
		}
		if lit, ok := extractGuardedLiteral(lines[j]); ok {
			// Two literals will be matched: the guard arm and the
			// export arm. They must agree.
			if guardLit == "" && pathImportGuardedCaseRe.MatchString(lines[j]) {
				guardLit = lit
				continue
			}
			if exportLit == "" && pathImportGuardedExportRe.MatchString(lines[j]) {
				exportLit = lit
				continue
			}
		}
	}
	if end < 0 || guardLit == "" || exportLit == "" {
		return nil, 0
	}
	if guardLit != exportLit {
		// Mismatched literals: spec says fall through to UNKNOWN. We
		// return nil so the caller doesn't consume the block as
		// IMPORTABLE; the head `case` line then lands in UNKNOWN.
		return nil, 0
	}
	raw := strings.Join(lines[i:end+1], "\n")
	return &classifiedGuardedBlock{
		StartLine: i + 1, // 1-based
		EndLine:   end + 1,
		Raw:       raw,
		Dir:       guardLit,
	}, end - i + 1
}

// classifyUnknownReason gives a short hint for why an UNKNOWN line was
// flagged, so the user has something to grep for.
func classifyUnknownReason(line string) string {
	if strings.Contains(line, "$(") || strings.Contains(line, "`") {
		return "command-substitution"
	}
	if strings.HasPrefix(strings.TrimSpace(line), "case ") {
		return "guarded-block-mismatch"
	}
	if strings.Contains(line, "${") {
		return "parameter-expansion"
	}
	if strings.HasPrefix(strings.TrimSpace(line), "if ") ||
		strings.HasPrefix(strings.TrimSpace(line), "[[") ||
		strings.HasPrefix(strings.TrimSpace(line), "[ ") {
		return "conditional"
	}
	return "path-touches-unknown-shape"
}

// scanPathImport walks the rc file content, classifies every relevant
// line, and produces the structured report. `file` is recorded into the
// report's `file` field verbatim (already an absolute path at this
// point).
func scanPathImport(file string, content []byte) pathImportReport {
	report := pathImportReport{File: file}
	if len(content) == 0 {
		return report
	}

	// Locate the byte ranges occupied by every managed block so we can
	// (a) emit one SKIP entry per block instead of one per body line,
	// and (b) suppress any IMPORTABLE/UNKNOWN classification of lines
	// inside a managed block (the body iterates dirs we already own).
	managed := findPathManagedEntries(content)

	// Build a (line-start byte offset → line index) map by splitting
	// once. We keep both the line strings and the byte offset of each
	// line's first byte.
	var lines []string
	var lineStartBytes []int
	{
		off := 0
		for off <= len(content) {
			j := off
			for j < len(content) && content[j] != '\n' {
				j++
			}
			lines = append(lines, string(content[off:j]))
			lineStartBytes = append(lineStartBytes, off)
			if j >= len(content) {
				break
			}
			off = j + 1
		}
	}

	// Determine which line-indexes (0-based) are inside a managed
	// block — those lines are "covered" and must not produce any
	// IMPORTABLE/UNKNOWN entries.
	covered := make([]bool, len(lines))
	for _, e := range managed {
		startLine, endLine := -1, -1
		for i, off := range lineStartBytes {
			if startLine == -1 && off == e.OpenStart {
				startLine = i
			}
			if endLine == -1 && off == e.CloseStart {
				endLine = i
			}
		}
		if startLine < 0 || endLine < 0 {
			continue
		}
		for i := startLine; i <= endLine; i++ {
			covered[i] = true
		}
		report.SkippedManaged = append(report.SkippedManaged, pathImportEntry{
			Line:      startLine + 1,
			Raw:       lines[startLine],
			MarkerID:  e.Marker.ID,
			Direction: e.Marker.Direction,
			DirCount:  len(e.Marker.Dirs),
		})
	}

	// dedup map for IMPORTABLE entries: normalized-dir → 1-based line.
	importedSeen := make(map[string]int)

	for i := 0; i < len(lines); i++ {
		if covered[i] {
			continue
		}
		raw := lines[i]
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "#!") {
			continue
		}

		// Try guarded block first: it can span multiple lines, and a
		// successful parse should consume them all so we don't
		// double-count the inner export line as a bare IMPORTABLE.
		if block, consumed := tryParseGuardedBlock(lines, i); block != nil {
			entry := pathImportEntry{
				Line:      block.StartLine,
				EndLine:   block.EndLine,
				Raw:       block.Raw,
				Dir:       block.Dir,
				Direction: pathDirectionPrepend,
				Shape:     pathImportShapeGuarded,
			}
			normalized := normalizePathDir(block.Dir)
			if first, ok := importedSeen[normalized]; ok {
				entry.dupOf = first
			} else {
				importedSeen[normalized] = block.StartLine
			}
			report.Importable = append(report.Importable, entry)
			i += consumed - 1 // for-loop will i++
			continue
		}

		// Bare IMPORTABLE.
		if dir, ok := classifyImportableBare(raw); ok {
			entry := pathImportEntry{
				Line:      i + 1,
				EndLine:   i + 1,
				Raw:       raw,
				Dir:       dir,
				Direction: pathDirectionPrepend,
				Shape:     pathImportShapeBare,
			}
			normalized := normalizePathDir(dir)
			if first, ok := importedSeen[normalized]; ok {
				entry.dupOf = first
			} else {
				importedSeen[normalized] = i + 1
			}
			report.Importable = append(report.Importable, entry)
			continue
		}

		// Dynamic.
		if reason := classifyDynamic(raw); reason != "" {
			report.SkippedDynamic = append(report.SkippedDynamic, pathImportEntry{
				Line:   i + 1,
				Raw:    raw,
				Reason: reason,
			})
			continue
		}

		// Touches PATH but didn't match anything: UNKNOWN.
		if pathImportTouchesPATH.MatchString(raw) {
			report.SkippedUnknown = append(report.SkippedUnknown, pathImportEntry{
				Line:   i + 1,
				Raw:    raw,
				Reason: classifyUnknownReason(raw),
			})
			continue
		}
	}

	// Proposal: one prepend with the unique IMPORTABLE dirs in
	// first-occurrence order.
	if firsts := uniqueImportableDirs(report.Importable); len(firsts) > 0 {
		report.Proposal = &pathImportProposal{
			Direction: pathDirectionPrepend,
			Dirs:      firsts,
		}
	}

	// Sort the managed/dynamic/unknown sections by line for
	// determinism. (Importable is already in scan order, which is what
	// we want.)
	sort.SliceStable(report.SkippedManaged, func(a, b int) bool {
		return report.SkippedManaged[a].Line < report.SkippedManaged[b].Line
	})
	sort.SliceStable(report.SkippedDynamic, func(a, b int) bool {
		return report.SkippedDynamic[a].Line < report.SkippedDynamic[b].Line
	})
	sort.SliceStable(report.SkippedUnknown, func(a, b int) bool {
		return report.SkippedUnknown[a].Line < report.SkippedUnknown[b].Line
	})

	return report
}

// uniqueImportableDirs returns the user-literal dirs from the IMPORTABLE
// section, dropping duplicates by normalized form, in first-occurrence
// order.
func uniqueImportableDirs(entries []pathImportEntry) []string {
	seen := make(map[string]bool)
	var out []string
	for _, e := range entries {
		if e.dupOf != 0 {
			continue
		}
		n := normalizePathDir(e.Dir)
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, e.Dir)
	}
	return out
}

// renderPathImportHuman writes the human-readable report to `w`. The
// shape matches spec §Output — empty sections are omitted, and if
// IMPORTABLE is empty we emit the "no importable PATH lines found"
// sentinel and skip the proposal entirely. The caller is responsible
// for printing any follow-on prompt/apply summary AFTER the report.
func renderPathImportHuman(w *strings.Builder, report pathImportReport) {
	fmt.Fprintf(w, "scanning %s...\n\n", report.File)

	if len(report.Importable) == 0 &&
		len(report.SkippedDynamic) == 0 &&
		len(report.SkippedManaged) == 0 &&
		len(report.SkippedUnknown) == 0 {
		w.WriteString("no importable PATH lines found.\n")
		return
	}

	if len(report.Importable) > 0 {
		w.WriteString("  IMPORTABLE (static, literal dir tokens):\n")
		for _, e := range report.Importable {
			label := e.Raw
			if e.Shape == pathImportShapeGuarded {
				label = fmt.Sprintf("case-guard for %s", e.Dir)
			}
			fmt.Fprintf(w, "    line %3d:  %s", e.Line, label)
			if e.dupOf != 0 {
				fmt.Fprintf(w, "  [duplicate of line %d]", e.dupOf)
			}
			w.WriteByte('\n')
		}
		w.WriteByte('\n')
	}

	if len(report.SkippedDynamic) > 0 {
		w.WriteString("  SKIPPED (dynamic — runs code at startup):\n")
		for _, e := range report.SkippedDynamic {
			fmt.Fprintf(w, "    line %3d:  %s        [%s]\n", e.Line, e.Raw, e.Reason)
		}
		w.WriteByte('\n')
	}

	if len(report.SkippedManaged) > 0 {
		w.WriteString("  SKIPPED (already dfm-managed):\n")
		for _, e := range report.SkippedManaged {
			fmt.Fprintf(w, "    line %3d:  # dfm:path:%s (%s, %d dirs)\n",
				e.Line, e.MarkerID, e.Direction, e.DirCount)
		}
		w.WriteByte('\n')
	}

	if len(report.SkippedUnknown) > 0 {
		w.WriteString("  SKIPPED (unknown shape — left alone):\n")
		for _, e := range report.SkippedUnknown {
			fmt.Fprintf(w, "    line %3d:  %s           [%s]\n", e.Line, e.Raw, e.Reason)
		}
		w.WriteByte('\n')
	}

	if report.Proposal != nil {
		fmt.Fprintf(w, "proposal: fold %d dirs into one %s entry:\n  %s\n\n",
			len(report.Proposal.Dirs),
			report.Proposal.Direction,
			strings.Join(report.Proposal.Dirs, ", "),
		)
	} else {
		// Sections emitted but nothing IMPORTABLE → no proposal line.
		if len(report.Importable) == 0 {
			w.WriteString("no importable PATH lines found.\n")
		}
	}
}

// newPathImportCmd builds the `dfm path import` subcommand.
//
// Behavior matrix:
//   - default (no --dry-run, no --yes): print proposal, prompt
//     `Apply? [y/N]:`, apply on y/yes (case-insensitive).
//   - --dry-run: print proposal, exit. No prompt, no write.
//   - --yes/-y: print proposal, skip prompt, apply unconditionally.
//   - --dry-run --yes: --dry-run wins.
//   - --json: emits the structured proposal; for apply mode an extra
//     trailing JSON object on a new line reports the outcome.
//
// Non-interactive stdin + no --yes + no --dry-run refuses with
// exitResolveErr — the user has to opt in explicitly when there's no
// TTY to read y/N from. --shell=fish remains rejected until the fish
// parser ships.
func newPathImportCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		dryRun    bool
		yes       bool
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Scan a tracked rc file for static PATH lines and fold them into one dfm-managed prepend entry",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if shellFlag != "" && fileFlag != "" {
				return exitf(exitResolveErr,
					"path import: --shell and --file are mutually exclusive")
			}
			if shellFlag == "fish" {
				return exitf(exitResolveErr,
					"path import: fish import not yet supported")
			}

			target, family, err := resolveAliasTarget(shellFlag, fileFlag)
			if err != nil {
				return err
			}

			// Read the rc bytes. Missing file is treated as "empty" so
			// the user sees the sentinel rather than a stat error.
			data, rerr := os.ReadFile(target)
			if rerr != nil {
				if os.IsNotExist(rerr) {
					data = nil
				} else {
					return fmt.Errorf("read %s: %w", target, rerr)
				}
			}

			report := scanPathImport(target, data)

			// Render the proposal (same as PR #49 for both modes).
			if asJSON {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(jsonReportShape(report)); err != nil {
					return err
				}
			} else {
				var b strings.Builder
				renderPathImportHuman(&b, report)
				if _, err := c.OutOrStdout().Write([]byte(b.String())); err != nil {
					return err
				}
			}

			// Dry-run wins over --yes per the surface matrix.
			if dryRun {
				return nil
			}

			// Nothing to import — short-circuit before snapshot/prompt.
			if report.Proposal == nil || len(report.Proposal.Dirs) == 0 {
				if asJSON {
					if err := writePathImportTrailer(c.OutOrStdout(), pathImportApplyResult{
						Applied: false,
						Reason:  "nothing-to-import",
					}); err != nil {
						return err
					}
				}
				return nil
			}

			// Determine whether to apply.
			apply := yes
			if !apply {
				if !isInteractiveStdin(c.InOrStdin()) {
					return exitf(exitResolveErr,
						"path import: non-interactive shell — pass --yes to apply or --dry-run to preview")
				}
				// Prompt goes to STDERR so --json's stdout stays a
				// clean JSON stream (proposal doc + one-line trailer).
				answer, perr := promptApply(c.ErrOrStderr(), c.InOrStdin())
				if perr != nil {
					return fmt.Errorf("read confirmation: %w", perr)
				}
				apply = answer
			}

			if !apply {
				if asJSON {
					if err := writePathImportTrailer(c.OutOrStdout(), pathImportApplyResult{
						Applied: false,
						Reason:  "user-declined",
					}); err != nil {
						return err
					}
				} else {
					fmt.Fprintln(c.OutOrStdout(), "declined — no changes written.")
				}
				return nil
			}

			// Apply path.
			return runPathImportApply(c, target, family, data, report)
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the proposal without writing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "apply without prompting (non-interactive)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a JSON report instead of human-readable text")
	return cmd
}

// pathImportApplyResult is the trailing JSON object emitted in --json
// mode after the structured proposal. It mirrors the human-readable
// summary the apply path prints: outcome + identifying ids.
type pathImportApplyResult struct {
	Applied    bool   `json:"applied"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	MarkerID   string `json:"marker_id,omitempty"`
	DirsCount  int    `json:"dirs_count,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// writePathImportTrailer encodes the apply-result JSON object as a
// single line so the dry-run proposal stays a standalone document on
// stdout and the trailer is unambiguously separable by line-based
// consumers.
func writePathImportTrailer(w io.Writer, res pathImportApplyResult) error {
	buf, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if _, err := w.Write(buf); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

// isInteractiveStdin reports whether r is something we can safely
// read a y/N answer from.
//
// Production:
//   - r is os.Stdin (or another *os.File). We treat it as interactive
//     iff its mode carries ModeCharDevice — i.e. a real TTY. Pipes,
//     redirected files, and closed fds all fail this check and bounce
//     out with the non-interactive error.
//
// Tests:
//   - cmd.SetIn(strings.NewReader(...)) injects a non-*os.File reader.
//     We treat that as interactive — the test author deliberately wired
//     up an input source and wants us to read it. Tests that DO want
//     to exercise the non-TTY refusal pass an os.Pipe() read-end via
//     SetIn; that's a *os.File without ModeCharDevice and falls into
//     the same branch as a production pipe.
func isInteractiveStdin(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// promptApply emits the Apply? [y/N]: prompt and reads a single line
// from r. Returns true on y/yes/Y/YES, false on anything else (including
// empty Enter). The prompt goes to w (typically stdout) so it lands in
// the same stream as the proposal — tests assert against stdout.
func promptApply(w io.Writer, r io.Reader) (bool, error) {
	if _, err := fmt.Fprint(w, "Apply? [y/N]: "); err != nil {
		return false, err
	}
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	line = strings.TrimSpace(line)
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// runPathImportApply performs the mutation: refuses if a corruption-
// shape prepend entry already exists, deletes the IMPORTABLE lines/
// blocks, splices in a new managed prepend block at the position of
// the first deleted importable line, snapshots + atomic-writes +
// records the hash change in the standard alias.add shape.
//
// `current` is the pre-edit file bytes (the caller already read them);
// passing them in avoids a second os.ReadFile and keeps the
// "what we hashed pre-write" semantics aligned with the snapshot
// that's about to be taken.
func runPathImportApply(c *cobra.Command, target, family string, current []byte, report pathImportReport) error {
	// Corruption-shape refusal: if rc already has a direction=prepend
	// dfm-managed entry, refuse — path add is the correct way to add
	// dirs to an existing managed entry.
	for _, e := range findPathManagedEntries(current) {
		if e.Marker.Direction == pathDirectionPrepend {
			return exitf(exitResolveErr,
				"path import: rc file already has a dfm-managed prepend entry — use 'dfm path add' to add dirs instead, or 'dfm path remove' first")
		}
	}

	// We can only mutate a tracked file — the audit + snapshot machinery
	// is keyed on tracker.File. Resolve once the user has confirmed.
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
	info, err := os.Stat(canonical)
	if err != nil {
		return fmt.Errorf("stat %s: %w", canonical, err)
	}

	dirs := report.Proposal.Dirs
	newID := pathMarkerID(pathDirectionPrepend, dirs)
	newBlock := renderPathBlock(family, newID, time.Now().UTC(), pathDirectionPrepend, dirs)

	newContent := spliceImportableLines(current, report.Importable, newBlock)

	mgr, mgrErr := newSnapshotManager(c.Context(), s)
	if mgrErr != nil {
		return fmt.Errorf("snapshot manager: %w", mgrErr)
	}
	snap, err := snapshot.TakePreEdit(c.Context(), mgr, canonical, file)
	if err != nil {
		return err
	}

	if err := fsx.AtomicWrite(canonical, newContent, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", canonical, err)
	}

	sum := sha256.Sum256(newContent)
	newHash := hex.EncodeToString(sum[:])

	// Audit field shape mirrors path.add: marker_id_new / marker_id_old /
	// snapshot_id are the same triple. lines_removed counts importable
	// entries folded into the new block. sub_action distinguishes
	// import from add/remove.
	if err := tracker.RecordHashChange(c.Context(), s, file, newHash, snap.ID, "path.import", map[string]any{
		"marker_id_new": newID,
		"marker_id_old": "",
		"snapshot_id":   snap.ID,
		"lines_removed": len(report.Importable),
		"dirs_count":    len(dirs),
		"direction":     pathDirectionPrepend,
		"sub_action":    "import",
	}); err != nil {
		return err
	}

	asJSON, _ := c.Flags().GetBool("json")
	if asJSON {
		return writePathImportTrailer(c.OutOrStdout(), pathImportApplyResult{
			Applied:    true,
			SnapshotID: snap.ID,
			MarkerID:   newID,
			DirsCount:  len(dirs),
		})
	}

	fmt.Fprintf(c.OutOrStdout(),
		"applied: %d dirs folded into one prepend entry (marker %s)\n",
		len(dirs), newID)
	fmt.Fprintf(c.OutOrStdout(),
		"snapshot: %s  ->  dfm restore --snapshot %s  to revert\n",
		snap.ID, snap.ID)
	return nil
}

// spliceImportableLines deletes every IMPORTABLE line/block from
// `current` (by 1-based line range stored on each entry) and inserts
// `newBlock` at the position of the FIRST deleted IMPORTABLE line.
// Consecutive blank lines that remain after deletion are trimmed to
// at most one. The original file's final-newline state is preserved
// byte-for-byte: if the input ended with "\n", so does the output;
// otherwise the output's last byte stays non-"\n".
func spliceImportableLines(current []byte, importable []pathImportEntry, newBlock string) []byte {
	if len(importable) == 0 {
		return current
	}
	hadTrailingNewline := len(current) > 0 && current[len(current)-1] == '\n'

	// Split into lines. We strip a single trailing "\n" if present so
	// the slice length equals the number of "real" lines and we can
	// rebuild with a join.
	src := current
	if hadTrailingNewline {
		src = src[:len(src)-1]
	}
	lines := strings.Split(string(src), "\n")

	// Mark which lines (0-based) to delete and find the first
	// IMPORTABLE line's index for the insertion point.
	toDelete := make([]bool, len(lines))
	firstIdx := -1
	for _, e := range importable {
		start := e.Line - 1
		end := e.EndLine - 1
		if end < start {
			end = start
		}
		for i := start; i <= end && i < len(lines); i++ {
			toDelete[i] = true
		}
		if firstIdx == -1 || start < firstIdx {
			firstIdx = start
		}
	}
	if firstIdx < 0 {
		// Defensive: nothing matched, return unchanged.
		return current
	}

	// Build the new line list. The new block is inserted as a single
	// pseudo-line at firstIdx (it already carries its own trailing
	// newline from renderPathBlock, so we re-split it into lines for
	// uniform handling and then trim the final empty slice element
	// produced by the trailing "\n").
	blockLines := strings.Split(strings.TrimSuffix(newBlock, "\n"), "\n")

	out := make([]string, 0, len(lines)+len(blockLines))
	inserted := false
	for i, ln := range lines {
		if i == firstIdx {
			out = append(out, blockLines...)
			inserted = true
		}
		if !toDelete[i] {
			out = append(out, ln)
		}
	}
	if !inserted {
		// firstIdx was past EOF — just append.
		out = append(out, blockLines...)
	}

	// Collapse runs of >1 blank line to exactly one.
	out = collapseBlankRuns(out)

	joined := strings.Join(out, "\n")
	if hadTrailingNewline {
		joined += "\n"
	}
	return []byte(joined)
}

// collapseBlankRuns reduces any run of >1 empty string in lines to a
// single empty string. Lines containing only whitespace are NOT treated
// as blank — they're rare in rc files and conservatively preserving
// them avoids surprising the user.
func collapseBlankRuns(lines []string) []string {
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, ln := range lines {
		if ln == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
		} else {
			prevBlank = false
		}
		out = append(out, ln)
	}
	return out
}


// jsonReportShape strips internal-only fields (dupOf) from the report
// before JSON encoding so the schema stays flat.
func jsonReportShape(report pathImportReport) pathImportReport {
	clean := func(in []pathImportEntry) []pathImportEntry {
		if in == nil {
			return []pathImportEntry{}
		}
		out := make([]pathImportEntry, len(in))
		for i, e := range in {
			e.dupOf = 0
			out[i] = e
		}
		return out
	}
	return pathImportReport{
		File:           report.File,
		Importable:     clean(report.Importable),
		SkippedDynamic: clean(report.SkippedDynamic),
		SkippedManaged: clean(report.SkippedManaged),
		SkippedUnknown: clean(report.SkippedUnknown),
		Proposal:       report.Proposal,
	}
}
