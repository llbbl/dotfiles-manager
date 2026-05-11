// Package diffrender renders unified-diff bodies with ANSI coloring.
package diffrender

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	green = "\x1b[32m"
	red   = "\x1b[31m"
	cyan  = "\x1b[36m"
	reset = "\x1b[0m"
)

// IsTerminal reports whether w is an interactive terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// WriteColored writes diff to w, coloring header/added/removed lines when
// w looks like a terminal. Non-terminals get the raw bytes.
func WriteColored(w io.Writer, diff string) {
	if !IsTerminal(w) {
		fmt.Fprintln(w, diff)
		return
	}
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"),
			strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "diff "),
			strings.HasPrefix(line, "@@"):
			fmt.Fprintln(w, cyan+line+reset)
		case strings.HasPrefix(line, "+"):
			fmt.Fprintln(w, green+line+reset)
		case strings.HasPrefix(line, "-"):
			fmt.Fprintln(w, red+line+reset)
		default:
			fmt.Fprintln(w, line)
		}
	}
}
