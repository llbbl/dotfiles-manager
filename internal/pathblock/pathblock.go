// Package pathblock renders, parses and removes dfm-managed PATH blocks
// and startup hooks in shell rc file content.
package pathblock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DirectionPrepend and DirectionAppend are the canonical marker direction tokens.
const (
	DirectionPrepend = "prepend"
	DirectionAppend  = "append"
)

// MarkerID hashes the direction and colon-joined directory tokens into eight hex characters.
func MarkerID(direction string, dirs []string) string {
	sum := sha256.Sum256([]byte(direction + ":" + strings.Join(dirs, ":")))
	return hex.EncodeToString(sum[:])[:8]
}

// Marker retains the opening line in Raw without its trailing newline for verbatim reuse.
type Marker struct {
	ID        string
	UpdatedAt time.Time
	Direction string
	Dirs      []string
	Raw       string
}

// Entry records byte offsets for a paired block. OpenEnd and CloseEnd exclude
// newlines; BlockEnd includes the closing newline when present.
type Entry struct {
	Marker     Marker
	OpenStart  int
	OpenEnd    int
	CloseStart int
	CloseEnd   int
	BlockStart int
	BlockEnd   int
}

// FormatOpen emits double-space-separated metadata without a trailing newline.
func FormatOpen(id string, updatedAt time.Time, direction string, dirs []string) string {
	return fmt.Sprintf(
		"# dfm:path:%s >>>  updated=%s  direction=%s  dirs=%s",
		id,
		updatedAt.UTC().Format(time.RFC3339),
		direction,
		strings.Join(dirs, ":"),
	)
}

// FormatClose emits the ID-only closing marker without a trailing newline.
func FormatClose(id string) string {
	return fmt.Sprintf("# dfm:path:%s <<<", id)
}

// openMarkerRe tolerates indentation and trailing whitespace around marker metadata.
var openMarkerRe = regexp.MustCompile(
	`^\s*# dfm:path:([0-9a-f]{8}) >>>\s+updated=(\S+)\s+direction=(\S+)\s+dirs=(.*?)\s*$`,
)

// closeMarkerRe captures the closing marker's ID.
var closeMarkerRe = regexp.MustCompile(
	`^\s*# dfm:path:([0-9a-f]{8}) <<<\s*$`,
)

// ParseOpen accepts markers with or without line terminators; an empty dirs field yields nil.
func ParseOpen(line string) (Marker, bool) {
	// Strip a trailing CRLF or LF so the regex's `$` matches whether
	// the caller fed us the line with or without its terminator.
	line = strings.TrimRight(line, "\r\n")
	m := openMarkerRe.FindStringSubmatch(line)
	if m == nil {
		return Marker{}, false
	}
	updated, err := time.Parse(time.RFC3339, m[2])
	if err != nil {
		return Marker{}, false
	}
	var dirs []string
	if m[4] != "" {
		dirs = strings.Split(m[4], ":")
	}
	return Marker{
		ID:        m[1],
		UpdatedAt: updated.UTC(),
		Direction: m[3],
		Dirs:      dirs,
		Raw:       line,
	}, true
}

// ParseClose accepts closing markers with or without line terminators.
func ParseClose(line string) (string, bool) {
	line = strings.TrimRight(line, "\r\n")
	m := closeMarkerRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// NormalizeDir expands leading HOME forms and strips trailing slashes for comparison.
// Persist the caller's literal token instead of this normalized value.
func NormalizeDir(dir string) string {
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

// FindEntries pairs each opening marker with the next unused closing marker of the same ID.
// Unpaired markers are ignored, and block bodies are opaque.
func FindEntries(content []byte) []Entry {
	type openCandidate struct {
		marker     Marker
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
		if marker, ok := ParseOpen(line); ok {
			opens = append(opens, openCandidate{marker: marker, start: i, end: j})
		} else if id, ok := ParseClose(line); ok {
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

	out := make([]Entry, 0, len(opens))
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
		out = append(out, Entry{
			Marker:     op.marker,
			OpenStart:  op.start,
			OpenEnd:    op.end,
			CloseStart: cl.start,
			CloseEnd:   cl.end,
			BlockStart: op.start,
			BlockEnd:   blockEnd,
		})
	}
	return out
}

// BuildPosixLine guards separators so reinserting a directory cannot add an empty PATH component.
func BuildPosixLine(direction string) string {
	if direction == DirectionAppend {
		return `PATH="${__dfm_new:+$__dfm_new:}$__dfm_d"`
	}
	return `PATH="$__dfm_d${__dfm_new:+:$__dfm_new}"`
}

// RenderBash emits POSIX shell syntax with a trailing newline after the closing marker.
// Directory tokens must be shell words without spaces, colons or shell metacharacters.
func RenderBash(id string, updatedAt time.Time, direction string, dirs []string) string {
	var b strings.Builder
	b.WriteString(FormatOpen(id, updatedAt, direction, dirs))
	b.WriteByte('\n')
	fmt.Fprintf(&b, "for __dfm_d in %s; do\n", strings.Join(dirs, " "))
	b.WriteString("  __dfm_rest=$PATH; __dfm_new=\n")
	b.WriteString("  while [ -n \"$__dfm_rest\" ]; do\n")
	b.WriteString("    __dfm_p=${__dfm_rest%%:*}\n")
	b.WriteString("    case $__dfm_rest in\n")
	b.WriteString("      *:*) __dfm_rest=${__dfm_rest#*:} ;;\n")
	b.WriteString("      *) __dfm_rest= ;;\n")
	b.WriteString("    esac\n")
	b.WriteString("    [ \"$__dfm_p\" = \"$__dfm_d\" ] || __dfm_new=${__dfm_new:+$__dfm_new:}$__dfm_p\n")
	b.WriteString("  done\n")
	fmt.Fprintf(&b, "  %s\n", BuildPosixLine(direction))
	b.WriteString("done\n")
	b.WriteString("unset __dfm_d __dfm_p __dfm_new __dfm_rest\n")
	b.WriteString("export PATH\n")
	b.WriteString(FormatClose(id))
	b.WriteByte('\n')
	return b.String()
}

// BuildZshLine filters through a variable so zsh matches the directory literally.
// It avoids a persistent uniqueness flag on the user's path array.
func BuildZshLine(direction string) string {
	if direction == DirectionAppend {
		return `path=(${path:#$__dfm_d} $__dfm_d)`
	}
	return `path=($__dfm_d ${path:#$__dfm_d})`
}

// RenderZsh uses zsh's native path array with the same marker and token contract as RenderBash.
func RenderZsh(id string, updatedAt time.Time, direction string, dirs []string) string {
	var b strings.Builder
	b.WriteString(FormatOpen(id, updatedAt, direction, dirs))
	b.WriteByte('\n')
	b.WriteString("# Assumes zsh's default tie between $path and $PATH, so no export is needed.\n")
	fmt.Fprintf(&b, "for __dfm_d in %s; do\n", strings.Join(dirs, " "))
	fmt.Fprintf(&b, "  %s\n", BuildZshLine(direction))
	b.WriteString("done\n")
	b.WriteString("unset __dfm_d\n")
	b.WriteString(FormatClose(id))
	b.WriteByte('\n')
	return b.String()
}

// BuildFishLine uses --path to avoid universal persistence and --move to promote existing directories.
func BuildFishLine(direction string) string {
	if direction == DirectionAppend {
		return "fish_add_path --path --move --append $__dfm_d"
	}
	return "fish_add_path --path --move $__dfm_d"
}

// RenderFish uses fish_add_path for idempotence and requires fish 3.2 or newer.
func RenderFish(id string, updatedAt time.Time, direction string, dirs []string) string {
	var b strings.Builder
	b.WriteString(FormatOpen(id, updatedAt, direction, dirs))
	b.WriteByte('\n')
	fmt.Fprintf(&b, "for __dfm_d in %s\n", strings.Join(dirs, " "))
	fmt.Fprintf(&b, "    %s\n", BuildFishLine(direction))
	b.WriteString("end\n")
	b.WriteString("set -e __dfm_d\n")
	b.WriteString(FormatClose(id))
	b.WriteByte('\n')
	return b.String()
}

// Render selects fish, zsh or POSIX syntax; unknown families use POSIX syntax.
func Render(family, id string, updatedAt time.Time, direction string, dirs []string) string {
	switch family {
	case "fish":
		return RenderFish(id, updatedAt, direction, dirs)
	case "zsh":
		return RenderZsh(id, updatedAt, direction, dirs)
	default:
		return RenderBash(id, updatedAt, direction, dirs)
	}
}

// Row keeps the JSON and tabular path-list fields aligned.
type Row struct {
	Dir       string `json:"dir"`
	Direction string `json:"direction"`
	MarkerID  string `json:"marker_id"`
}

// ListRows emits one row per directory, with prepend entries before append entries.
func ListRows(entries []Entry) []Row {
	var prepend, appendRows []Row
	for _, e := range entries {
		for _, d := range e.Marker.Dirs {
			row := Row{
				Dir:       d,
				Direction: e.Marker.Direction,
				MarkerID:  e.Marker.ID,
			}
			if e.Marker.Direction == DirectionAppend {
				appendRows = append(appendRows, row)
			} else {
				prepend = append(prepend, row)
			}
		}
	}
	return append(prepend, appendRows...)
}
