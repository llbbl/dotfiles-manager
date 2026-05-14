// Package wizard implements the interactive first-run setup flow for
// `dfm init`. It is decoupled from cobra and from any specific terminal
// so the chapters can be driven by tests with plain in-memory
// io.Reader / io.Writer values.
//
// The flow is split into chapters (config path, AI, state store, backup
// repo, track-nudge, summary). Each chapter consumes a *config.Config
// it can mutate and returns whatever side-effect intent it produced
// (e.g. "provision a Turso DB" or "track ~/.zshrc"). The cobra layer in
// cmd/dfm then executes those intents using the existing helpers.
package wizard

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// PromptOpts configures a single line-oriented prompt. Question is the
// human text printed to Out; Default is shown in [brackets] and is
// returned when the user presses Enter on an empty line. Validate is
// optional and, when supplied, the prompt re-asks until validation
// passes or the input stream is exhausted.
type PromptOpts struct {
	Question string
	Default  string
	Validate func(string) error
	// Secret hints to the prompt that the value is sensitive. We don't
	// implement terminal masking (we have no terminal); instead the
	// prompt does NOT echo the default in [brackets] when Secret is
	// true, so a token isn't accidentally printed to logs.
	Secret bool
}

// AskLine prints a prompt to out, reads one line from in, and returns
// the trimmed result (or the default when the line is empty). It is
// the single entry point for every interactive question the wizard
// asks, which keeps the I/O surface tiny and easy to mock in tests.
//
// in must be a *bufio.Reader so successive calls share buffering; the
// caller wraps os.Stdin once in cmd/dfm.
func AskLine(in *bufio.Reader, out io.Writer, p PromptOpts) (string, error) {
	for {
		if p.Secret {
			fmt.Fprintf(out, "%s: ", p.Question)
		} else if p.Default != "" {
			fmt.Fprintf(out, "%s [%s]: ", p.Question, p.Default)
		} else {
			fmt.Fprintf(out, "%s: ", p.Question)
		}

		line, err := in.ReadString('\n')
		// Treat io.EOF with no trailing data as "accept default": that
		// lets test inputs end cleanly without a final newline, and
		// matches what a real user pressing Ctrl-D after no input would
		// reasonably expect for a single prompt.
		if err != nil && err != io.EOF {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			line = p.Default
		}
		if p.Validate != nil {
			if verr := p.Validate(line); verr != nil {
				fmt.Fprintf(out, "  %v\n", verr)
				if err == io.EOF {
					return "", verr
				}
				continue
			}
		}
		return line, nil
	}
}

// AskYesNo is a thin wrapper around AskLine for binary choices. The
// default ("y" or "n") is shown capitalized in the prompt. Anything
// that doesn't look like yes is treated as no.
func AskYesNo(in *bufio.Reader, out io.Writer, question string, defaultYes bool) (bool, error) {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	fmt.Fprintf(out, "%s [%s]: ", question, hint)
	line, err := in.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	if a == "" {
		return defaultYes, nil
	}
	return a == "y" || a == "yes", nil
}
