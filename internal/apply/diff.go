package apply

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
)

// FileSet is a parsed unified-diff payload. The applier only accepts
// single-file diffs.
type FileSet struct {
	OldPath string
	NewPath string
	Hunks   []Hunk
}

// Hunk is one @@ block.
type Hunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []HunkLine
}

// HunkLine is one line inside a hunk. Kind is one of ' ', '+', '-'.
type HunkLine struct {
	Kind byte
	Text string
	// NoNewline is true when this line was followed by a
	// "\ No newline at end of file" directive.
	NoNewline bool
}

// Validate parses a unified diff. Returns ErrDiffEmpty if there is no
// content and ErrDiffMalformed for everything else that fails to parse,
// or if the diff covers more than one file pair.
func Validate(diff string) (FileSet, error) {
	if strings.TrimSpace(diff) == "" {
		return FileSet{}, ErrDiffEmpty
	}
	// Normalize CRLF in the diff payload itself; line-endings inside the
	// destination file are handled separately by ApplyToBytes.
	normalized := strings.ReplaceAll(diff, "\r\n", "\n")
	sc := bufio.NewScanner(strings.NewReader(normalized))
	sc.Buffer(make([]byte, 1<<20), 16<<20)

	var (
		fs            FileSet
		sawOld, sawNew bool
		curHunk       *Hunk
	)

	flushHunk := func() error {
		if curHunk == nil {
			return nil
		}
		fs.Hunks = append(fs.Hunks, *curHunk)
		curHunk = nil
		return nil
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "--- "):
			if sawOld {
				return FileSet{}, fmt.Errorf("%w: multiple file headers", ErrDiffMalformed)
			}
			fs.OldPath = strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			sawOld = true
		case strings.HasPrefix(line, "+++ "):
			if sawNew {
				return FileSet{}, fmt.Errorf("%w: multiple file headers", ErrDiffMalformed)
			}
			fs.NewPath = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			sawNew = true
		case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
			// Advisory; ignore.
		case strings.HasPrefix(line, "@@"):
			if err := flushHunk(); err != nil {
				return FileSet{}, err
			}
			h, err := parseHunkHeader(line)
			if err != nil {
				return FileSet{}, err
			}
			curHunk = &h
		case strings.HasPrefix(line, `\ `):
			// "\ No newline at end of file"
			if curHunk == nil || len(curHunk.Lines) == 0 {
				return FileSet{}, fmt.Errorf("%w: stray no-newline marker", ErrDiffMalformed)
			}
			curHunk.Lines[len(curHunk.Lines)-1].NoNewline = true
		default:
			if curHunk == nil {
				// Skip stray lines before the first hunk.
				continue
			}
			if line == "" {
				curHunk.Lines = append(curHunk.Lines, HunkLine{Kind: ' ', Text: ""})
				continue
			}
			switch line[0] {
			case ' ', '+', '-':
				curHunk.Lines = append(curHunk.Lines, HunkLine{Kind: line[0], Text: line[1:]})
			default:
				return FileSet{}, fmt.Errorf("%w: unexpected line prefix", ErrDiffMalformed)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return FileSet{}, fmt.Errorf("%w: scan: %v", ErrDiffMalformed, err)
	}
	if err := flushHunk(); err != nil {
		return FileSet{}, err
	}
	if !sawOld || !sawNew {
		return FileSet{}, fmt.Errorf("%w: missing file headers", ErrDiffMalformed)
	}
	if len(fs.Hunks) == 0 {
		return FileSet{}, fmt.Errorf("%w: no hunks", ErrDiffMalformed)
	}
	return fs, nil
}

func parseHunkHeader(line string) (Hunk, error) {
	// @@ -OLD_START,OLD_COUNT +NEW_START,NEW_COUNT @@ optional context
	bad := func() (Hunk, error) {
		return Hunk{}, fmt.Errorf("%w: bad hunk header %q", ErrDiffMalformed, line)
	}
	rest := strings.TrimPrefix(line, "@@")
	idx := strings.Index(rest, "@@")
	if idx < 0 {
		return bad()
	}
	spec := strings.TrimSpace(rest[:idx])
	parts := strings.Fields(spec)
	if len(parts) < 2 {
		return bad()
	}
	oldStart, oldCount, err := parseRange(parts[0], '-')
	if err != nil {
		return bad()
	}
	newStart, newCount, err := parseRange(parts[1], '+')
	if err != nil {
		return bad()
	}
	return Hunk{
		OldStart: oldStart, OldCount: oldCount,
		NewStart: newStart, NewCount: newCount,
	}, nil
}

func parseRange(s string, sign byte) (int, int, error) {
	if len(s) == 0 || s[0] != sign {
		return 0, 0, errors.New("bad range")
	}
	s = s[1:]
	start, count := s, "1"
	if i := strings.IndexByte(s, ','); i >= 0 {
		start, count = s[:i], s[i+1:]
	}
	a, err := atoi(start)
	if err != nil {
		return 0, 0, err
	}
	b, err := atoi(count)
	if err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

func atoi(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty number")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("bad number %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

const offsetTolerance = 3

// ApplyToBytes applies fs to orig and returns the new bytes plus the
// count of applied hunks. The applier preserves the file's existing line
// ending (LF or CRLF) on output. Refuses multi-file diffs.
func ApplyToBytes(orig []byte, fs FileSet) ([]byte, int, error) {
	if fs.OldPath == "" || fs.NewPath == "" {
		return nil, 0, fmt.Errorf("%w: missing file headers", ErrDiffMalformed)
	}

	lineSep := detectLineSep(orig)
	hadTrailing := len(orig) > 0 && (orig[len(orig)-1] == '\n')

	lines := splitLines(orig)

	applied := 0
	// `delta` tracks (lines added so far) - (lines removed so far) to
	// project each hunk's old_start onto the current `lines` slice.
	delta := 0
	for hi, h := range fs.Hunks {
		base := h.OldStart - 1 + delta // 0-based, projected
		offset, ok := findHunk(lines, base, h)
		if !ok {
			return nil, 0, fmt.Errorf("%w: hunk %d", ErrDiffDoesNotApply, hi+1)
		}
		insertAt := base + offset

		// Build replacement slice from the hunk.
		var removed, added int
		var replacement []string
		for _, hl := range h.Lines {
			switch hl.Kind {
			case ' ':
				replacement = append(replacement, hl.Text)
				removed++
				added++
			case '-':
				removed++
			case '+':
				replacement = append(replacement, hl.Text)
				added++
			}
		}
		// Splice.
		end := insertAt + removed
		if end > len(lines) {
			return nil, 0, fmt.Errorf("%w: hunk %d overruns file", ErrDiffDoesNotApply, hi+1)
		}
		newLines := make([]string, 0, len(lines)-removed+added)
		newLines = append(newLines, lines[:insertAt]...)
		newLines = append(newLines, replacement...)
		newLines = append(newLines, lines[end:]...)
		lines = newLines
		delta += added - removed
		applied++
	}

	// Determine trailing newline policy: scan the final hunk for any
	// "no newline" directive that applied to the new side.
	newTrailing := hadTrailing
	if len(fs.Hunks) > 0 {
		last := fs.Hunks[len(fs.Hunks)-1]
		// Find the final '+' or ' ' line — that's the last line on the
		// new side of the file. If it carried NoNewline, drop trailing.
		// Otherwise, if the last '-' line carried NoNewline and the
		// last '+' did not, add one back.
		var lastNew *HunkLine
		var lastOld *HunkLine
		for i := range last.Lines {
			l := &last.Lines[i]
			switch l.Kind {
			case ' ', '+':
				lastNew = l
			}
			switch l.Kind {
			case ' ', '-':
				lastOld = l
			}
		}
		if lastNew != nil && lastNew.NoNewline {
			newTrailing = false
		} else if lastNew != nil && lastOld != nil && lastOld.NoNewline && !lastNew.NoNewline {
			newTrailing = true
		}
	}

	out := strings.Join(lines, lineSep)
	if newTrailing && !strings.HasSuffix(out, lineSep) {
		out += lineSep
	}
	if !newTrailing && strings.HasSuffix(out, lineSep) {
		out = strings.TrimSuffix(out, lineSep)
	}
	return []byte(out), applied, nil
}

// findHunk returns the offset (in [-offsetTolerance, +offsetTolerance])
// at which the hunk's context+removal lines match the file. Returns
// (0, false) if no nearby position matches.
func findHunk(lines []string, base int, h Hunk) (int, bool) {
	// Build the slice of lines the hunk expects to see at `base`.
	var expect []string
	for _, hl := range h.Lines {
		if hl.Kind == ' ' || hl.Kind == '-' {
			expect = append(expect, hl.Text)
		}
	}
	if len(expect) == 0 {
		// Pure insertion at a position — tolerate offset zero only.
		if base >= 0 && base <= len(lines) {
			return 0, true
		}
		return 0, false
	}
	tryAt := func(start int) bool {
		if start < 0 || start+len(expect) > len(lines) {
			return false
		}
		for i, want := range expect {
			if lines[start+i] != want {
				return false
			}
		}
		return true
	}
	// Try 0 first, then expand outward.
	if tryAt(base) {
		return 0, true
	}
	for delta := 1; delta <= offsetTolerance; delta++ {
		if tryAt(base + delta) {
			return delta, true
		}
		if tryAt(base - delta) {
			return -delta, true
		}
	}
	return 0, false
}

// detectLineSep returns "\r\n" if the original looks CRLF, else "\n".
func detectLineSep(b []byte) string {
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			if i > 0 && b[i-1] == '\r' {
				return "\r\n"
			}
			return "\n"
		}
	}
	return "\n"
}

// splitLines splits on \n (after stripping a single trailing \n if
// present) and trims a trailing \r from each line.
func splitLines(b []byte) []string {
	s := string(b)
	hadTrailing := strings.HasSuffix(s, "\n")
	if hadTrailing {
		s = strings.TrimSuffix(s, "\n")
	}
	if s == "" {
		if hadTrailing {
			return []string{}
		}
		return []string{}
	}
	parts := strings.Split(s, "\n")
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}
