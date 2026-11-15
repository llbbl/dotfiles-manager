// Package pathimport scans shell rc content for importable PATH lines
// and renders reports and consolidated replacements.
package pathimport

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/pathblock"
)

// Shape distinguishes bare prepends from guarded blocks; each report bucket supplies its classification.
type Shape string

const (
	ShapeBare    Shape = "bare"
	ShapeGuarded Shape = "guarded"
)

// Entry records a classified line or block and retains the original text.
type Entry struct {
	Line int `json:"line"`
	// EndLine includes the closing esac for guarded blocks and is omitted from JSON.
	EndLine int    `json:"-"`
	Raw     string `json:"raw"`
	Dir     string `json:"dir,omitempty"`
	// Direction is prepend for importable entries and empty otherwise.
	Direction string `json:"direction,omitempty"`
	Shape     Shape  `json:"shape,omitempty"`

	// Reason carries the short hint for SKIP entries (dynamic/unknown).
	Reason string `json:"reason,omitempty"`

	// Managed-block metadata: only populated for "skipped_managed" entries.
	MarkerID string `json:"marker_id,omitempty"`
	DirCount int    `json:"dir_count,omitempty"`

	// dupOf points to the first line with the same normalized directory for human output only.
	dupOf int
}

// Report is the structured outcome of a single import scan.
type Report struct {
	File           string    `json:"file"`
	Importable     []Entry   `json:"importable"`
	SkippedDynamic []Entry   `json:"skipped_dynamic"`
	SkippedManaged []Entry   `json:"skipped_managed"`
	SkippedUnknown []Entry   `json:"skipped_unknown"`
	Proposal       *Proposal `json:"proposal,omitempty"`
}

// Proposal retains the first literal for each directory in a consolidated prepend entry.
type Proposal struct {
	Direction string   `json:"direction"`
	Dirs      []string `json:"dirs"`
}

// --- regex shapes (line-oriented; intentionally conservative) -------

// pathImportLiteralBareRe captures quoted absolute, HOME and tilde literals or bare $HOME literals.
// Its conservative character class rejects whitespace and command substitutions.
var pathImportLiteralBareRe = regexp.MustCompile(
	`^(?:export\s+)?PATH\s*=\s*` +
		`(?:` +
		`"((?:\$\{?HOME\}?|~)?/[A-Za-z0-9._\-/${}]+):\$PATH"` +
		`|` +
		`(\$HOME/[A-Za-z0-9._/-]+):\$PATH` +
		`)` +
		`\s*(?:;\s*export\s+PATH\s*)?$`,
)

// pathImportTouchesPATH catches PATH references that do not match a known shape.
var pathImportTouchesPATH = regexp.MustCompile(
	`(?:^|[^A-Za-z_])PATH(?:=|\s|:|$)`,
)

// pathImportEvalRe recognizes `eval "$(...)"` and `eval $(...)`. The
// arguments inside the substitution don't matter — we never evaluate.
var pathImportEvalRe = regexp.MustCompile(
	`^\s*eval\s+["']?\$\(`,
)

// pathImportSourceRe recognizes source and dot commands at the start of a line.
// classifyDynamic also checks guarded sources by substring.
var pathImportSourceRe = regexp.MustCompile(
	`^\s*(?:source|\.)\s+\S+`,
)

// pathImportGuardedExportRe captures the literal in a guarded export arm.
var pathImportGuardedExportRe = regexp.MustCompile(
	`\*\)\s*(?:export\s+)?PATH\s*=\s*"((?:\$\{?HOME\}?|~)?/?[A-Za-z0-9._\-/${}]+):\$PATH"\s*;;`,
)

// pathImportGuardedCaseRe captures the literal in a case guard arm.
var pathImportGuardedCaseRe = regexp.MustCompile(
	`\*":((?:\$\{?HOME\}?|~)?/?[A-Za-z0-9._\-/${}]+):"\*\)\s*;;`,
)

// pluginLoaderNames are matched case-insensitively to identify plugin loaders.
var pluginLoaderNames = []string{
	"oh-my-zsh.sh",
	"zinit",
	"antigen",
	"znap",
	"sheldon",
}

// --- helpers --------------------------------------------------------

// stripComment treats # as a comment only at the start of a line or after whitespace.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return strings.TrimRight(line[:i], " \t")
		}
	}
	return line
}

// classifyDynamic returns eval-activate, source or plugin-loader for recognized dynamic lines.
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

// classifyImportableBare returns an unquoted literal token without normalizing it.
func classifyImportableBare(line string) (string, bool) {
	t := strings.TrimSpace(stripComment(line))
	if t == "" {
		return "", false
	}
	// The regex itself accepts an optional trailing `; export PATH`.
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

// classifiedGuardedBlock retains the inclusive line range and literal of a recognized guard.
type classifiedGuardedBlock struct {
	StartLine int
	EndLine   int
	Raw       string
	Dir       string
}

// tryParseGuardedBlock accepts pnpm-style case guards whose guard and export literals agree.
// It returns the parsed block and consumed line count, or nil and zero.
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

// Scan classifies PATH-related lines without evaluating shell code.
// It records file verbatim and deduplicates proposal directories by normalized form.
func Scan(file string, content []byte) Report {
	report := Report{File: file}
	if len(content) == 0 {
		return report
	}

	// Locate the byte ranges occupied by every managed block so we can
	// (a) emit one SKIP entry per block instead of one per body line,
	// and (b) suppress any IMPORTABLE/UNKNOWN classification of lines
	// inside a managed block (the body iterates dirs we already own).
	managed := pathblock.FindEntries(content)

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
		report.SkippedManaged = append(report.SkippedManaged, Entry{
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
			entry := Entry{
				Line:      block.StartLine,
				EndLine:   block.EndLine,
				Raw:       block.Raw,
				Dir:       block.Dir,
				Direction: pathblock.DirectionPrepend,
				Shape:     ShapeGuarded,
			}
			normalized := pathblock.NormalizeDir(block.Dir)
			if first, ok := importedSeen[normalized]; ok {
				entry.dupOf = first
			} else {
				importedSeen[normalized] = block.StartLine
			}
			report.Importable = append(report.Importable, entry)
			i += consumed - 1 // for-loop will i++
			continue
		}

		if dir, ok := classifyImportableBare(raw); ok {
			entry := Entry{
				Line:      i + 1,
				EndLine:   i + 1,
				Raw:       raw,
				Dir:       dir,
				Direction: pathblock.DirectionPrepend,
				Shape:     ShapeBare,
			}
			normalized := pathblock.NormalizeDir(dir)
			if first, ok := importedSeen[normalized]; ok {
				entry.dupOf = first
			} else {
				importedSeen[normalized] = i + 1
			}
			report.Importable = append(report.Importable, entry)
			continue
		}

		if reason := classifyDynamic(raw); reason != "" {
			report.SkippedDynamic = append(report.SkippedDynamic, Entry{
				Line:   i + 1,
				Raw:    raw,
				Reason: reason,
			})
			continue
		}

		// Touches PATH but didn't match anything: UNKNOWN.
		if pathImportTouchesPATH.MatchString(raw) {
			report.SkippedUnknown = append(report.SkippedUnknown, Entry{
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
		report.Proposal = &Proposal{
			Direction: pathblock.DirectionPrepend,
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
func uniqueImportableDirs(entries []Entry) []string {
	seen := make(map[string]bool)
	var out []string
	for _, e := range entries {
		if e.dupOf != 0 {
			continue
		}
		n := pathblock.NormalizeDir(e.Dir)
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, e.Dir)
	}
	return out
}

// RenderHuman omits empty buckets and reports when no importable lines exist.
// The caller prints any confirmation prompt or apply summary after the report.
func RenderHuman(w *strings.Builder, report Report) {
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
			if e.Shape == ShapeGuarded {
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

// Splice replaces importable line ranges at the first removed line with newBlock.
// It collapses blank runs and preserves the original trailing-newline state.
func Splice(current []byte, importable []Entry, newBlock string) []byte {
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
	// newline from pathblock.Render, so we re-split it into lines for
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

// collapseBlankRuns retains one empty line per run and preserves whitespace-only lines.
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

// JSONShape strips internal-only fields (dupOf) from the report
// before JSON encoding so the schema stays flat.
func JSONShape(report Report) Report {
	clean := func(in []Entry) []Entry {
		if in == nil {
			return []Entry{}
		}
		out := make([]Entry, len(in))
		for i, e := range in {
			e.dupOf = 0
			out[i] = e
		}
		return out
	}
	return Report{
		File:           report.File,
		Importable:     clean(report.Importable),
		SkippedDynamic: clean(report.SkippedDynamic),
		SkippedManaged: clean(report.SkippedManaged),
		SkippedUnknown: clean(report.SkippedUnknown),
		Proposal:       report.Proposal,
	}
}
