package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

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
// sentinel and skip the proposal entirely.
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
		w.WriteString("(apply flow not yet implemented — see follow-up)\n")
	} else {
		// Sections emitted but nothing IMPORTABLE → no proposal line.
		if len(report.Importable) == 0 {
			w.WriteString("no importable PATH lines found.\n")
		}
	}
}

// newPathImportCmd builds the `dfm path import` subcommand.
//
// Read-only contract (PR1):
//   - --dry-run is REQUIRED. Running without it exits exitResolveErr
//     with the "apply flow not yet implemented" message.
//   - No snapshot, no audit, no tracker.RecordHashChange.
//   - The rc file is opened os.ReadFile only.
//   - --shell=fish exits exitResolveErr: the fish parser ships in a
//     follow-up.
func newPathImportCmd() *cobra.Command {
	var (
		shellFlag string
		fileFlag  string
		dryRun    bool
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Scan a tracked rc file for static PATH lines and propose consolidation (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if shellFlag != "" && fileFlag != "" {
				return exitf(exitResolveErr,
					"path import: --shell and --file are mutually exclusive")
			}
			if !dryRun {
				return exitf(exitResolveErr,
					"apply flow not yet implemented — re-run with --dry-run to see the proposal")
			}
			if shellFlag == "fish" {
				return exitf(exitResolveErr,
					"path import: fish import not yet supported")
			}

			target, _, err := resolveAliasTarget(shellFlag, fileFlag)
			if err != nil {
				return err
			}

			// import is read-only and tolerates an untracked rc file —
			// the tracker check would be load-bearing only if we were
			// about to mutate. Read the bytes and classify.
			data, rerr := os.ReadFile(target)
			if rerr != nil {
				if os.IsNotExist(rerr) {
					// Treat a missing rc file as "empty" so the user
					// sees the sentinel rather than a stat error.
					data = nil
				} else {
					return fmt.Errorf("read %s: %w", target, rerr)
				}
			}

			report := scanPathImport(target, data)

			if asJSON {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(jsonReportShape(report))
			}

			var b strings.Builder
			renderPathImportHuman(&b, report)
			_, err = c.OutOrStdout().Write([]byte(b.String()))
			return err
		},
	}
	cmd.Flags().StringVar(&shellFlag, "shell", "", "shell to target (bash|zsh|profile)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "explicit rc file (overrides --shell)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the proposal without writing (required in this version)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a JSON report instead of human-readable text")
	return cmd
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
