package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/pressly/goose/v3"
)

// gooseLogger adapts the pressly/goose v3 Logger interface onto our
// dlog package. The interface in goose/v3 is small:
//
//	Printf(format string, v ...any)
//	Fatalf(format string, v ...any)
//
// Without this adapter goose falls back to the stdlib `log` package,
// which prints unconditionally to stderr (e.g. "goose: no migrations to
// run. current version: 2"). That noise leaks past --verbose and past
// dlog. We route Printf at debug level so the message only surfaces
// when DFM_LOG_LEVEL=debug (or --verbose is set), and Fatalf at error
// level + os.Exit(1) so genuinely unrecoverable migration errors stay
// loud.
type gooseLogger struct {
	ctx context.Context
}

// newGooseLogger returns a goose.Logger adapter bound to ctx. The ctx
// is used to fetch the active *slog.Logger via dlog.From at log time;
// if no logger is attached, dlog.Discard is used.
func newGooseLogger(ctx context.Context) goose.Logger {
	if ctx == nil {
		ctx = context.Background()
	}
	return &gooseLogger{ctx: ctx}
}

// Printf routes goose's informational output through dlog at debug
// level. Trailing newlines from goose's format strings are trimmed.
func (g *gooseLogger) Printf(format string, v ...any) {
	msg := trimTrailingNewline(fmt.Sprintf(format, v...))
	dlog.From(g.ctx).Debug("goose: " + msg)
}

// Fatalf logs an error-level message and exits the process. Goose
// reserves Fatalf for unrecoverable migration failures; silencing it
// would hide real problems. We mirror stdlib log.Fatalf semantics:
// emit, then exit non-zero.
func (g *gooseLogger) Fatalf(format string, v ...any) {
	msg := trimTrailingNewline(fmt.Sprintf(format, v...))
	l := dlog.From(g.ctx)
	l.Error("goose: " + msg)
	// If dlog isn't carrying through error-level events (e.g. it's
	// the Discard logger in a test or off-by-default path), still
	// emit to stderr — an unrecoverable migration error must never
	// be silent.
	if !l.Enabled(g.ctx, slog.LevelError) {
		fmt.Fprintln(os.Stderr, "goose: "+msg)
	}
	os.Exit(1)
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
