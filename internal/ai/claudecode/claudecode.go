// Package claudecode implements an AI adapter that shells out to the
// `claude` CLI in one-shot mode (`-p <prompt> --output-format=json`).
package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// Error sentinels mirror the ones surfaced by the parent ai package.
// They are duplicated here to keep this subpackage free of cycles.
var (
	ErrEmptyResponse = errors.New("claudecode: empty response")
	ErrMalformedDiff = errors.New("claudecode: malformed diff")
)

// AskResult is the parsed output of a free-form question.
type AskResult struct {
	Text     string
	Duration time.Duration
}

// SuggestInput is the data the caller hands the adapter to produce a patch.
type SuggestInput struct {
	File    tracker.File
	Content []byte
	Goal    string
}

// SuggestResult carries the parsed diff and summary.
type SuggestResult struct {
	Prompt   string
	Diff     string
	Summary  string
	Duration time.Duration
}

// Runner runs a child process and returns its combined stdout bytes.
// Tests inject a fake; production uses execRun.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Adapter is the Claude Code provider implementation.
type Adapter struct {
	bin       string
	model     string
	extraArgs []string
	Runner    Runner
}

// New constructs an Adapter from the resolved config. Callers may
// replace Runner in tests.
func New(cfg *config.Config) *Adapter {
	bin := cfg.AI.ClaudeCode.Bin
	if bin == "" {
		bin = "claude"
	}
	return &Adapter{
		bin:       bin,
		model:     cfg.AI.ClaudeCode.Model,
		extraArgs: append([]string(nil), cfg.AI.ClaudeCode.ExtraArgs...),
		Runner:    execRun,
	}
}

// Name identifies the adapter.
func (a *Adapter) Name() string { return "claude-code" }

// BuildArgs returns the argv (without the bin) the adapter would pass
// for a given prompt. Exposed for tests.
func (a *Adapter) BuildArgs(prompt string) []string {
	args := []string{"-p", prompt, "--output-format=json"}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	args = append(args, a.extraArgs...)
	return args
}

// Ask sends a free-form prompt and returns the assistant's reply.
func (a *Adapter) Ask(ctx context.Context, prompt string) (AskResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return AskResult{}, errors.New("claudecode: empty prompt")
	}
	start := time.Now()
	argc := len(a.BuildArgs(prompt))
	text, err := a.run(ctx, prompt)
	dur := time.Since(start)
	if err != nil {
		dlog.From(ctx).Warn("claude failure", "argc", argc, "duration_ms", dur.Milliseconds())
		return AskResult{}, err
	}
	dlog.From(ctx).Debug("claude invocation", "argc", argc, "duration_ms", dur.Milliseconds())
	return AskResult{Text: text, Duration: dur}, nil
}

// Suggest renders the structured prompt, runs claude, and splits the
// result into a one-line summary and a unified diff.
func (a *Adapter) Suggest(ctx context.Context, in SuggestInput) (SuggestResult, error) {
	prompt := renderSuggestPrompt(in)
	start := time.Now()
	argc := len(a.BuildArgs(prompt))
	text, err := a.run(ctx, prompt)
	dur := time.Since(start)
	if err != nil {
		dlog.From(ctx).Warn("claude failure", "argc", argc, "duration_ms", dur.Milliseconds())
		return SuggestResult{}, err
	}
	summary, diff, err := splitSuggestResponse(text)
	if err != nil {
		dlog.From(ctx).Warn("claude failure", "argc", argc, "duration_ms", dur.Milliseconds())
		return SuggestResult{}, err
	}
	dlog.From(ctx).Debug("claude invocation", "argc", argc, "duration_ms", dur.Milliseconds())
	return SuggestResult{
		Prompt:   prompt,
		Summary:  summary,
		Diff:     diff,
		Duration: dur,
	}, nil
}

// run executes the CLI and pulls the assistant text out of the JSON.
func (a *Adapter) run(ctx context.Context, prompt string) (string, error) {
	args := a.BuildArgs(prompt)
	out, err := a.Runner(ctx, a.bin, args...)
	if err != nil {
		return "", fmt.Errorf("claudecode: run %s: %w", a.bin, err)
	}
	return parseResult(out)
}

// parseResult tolerates evolution of the claude --output-format=json
// shape. The expected key is "result" (string); we fall back to a small
// list of plausible aliases if it is missing.
func parseResult(raw []byte) (string, error) {
	trim := strings.TrimSpace(string(raw))
	if trim == "" {
		return "", fmt.Errorf("%w: no output from cli", ErrEmptyResponse)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trim), &obj); err != nil {
		return "", fmt.Errorf("%w: %s", ErrEmptyResponse, excerpt(trim))
	}
	keys := []string{"result", "response", "text", "content"}
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && strings.TrimSpace(s) != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %s", ErrEmptyResponse, excerpt(trim))
}

// splitSuggestResponse extracts the summary and diff sections produced
// by renderSuggestPrompt's template.
func splitSuggestResponse(text string) (summary, diff string, err error) {
	const (
		markSummary = "---SUMMARY---"
		markDiff    = "---DIFF---"
		markEnd     = "---END---"
	)
	iSum := strings.Index(text, markSummary)
	iDiff := strings.Index(text, markDiff)
	if iSum < 0 || iDiff < 0 || iDiff <= iSum {
		return "", "", fmt.Errorf("%w: missing markers: %s", ErrMalformedDiff, excerpt(text))
	}
	summary = strings.TrimSpace(text[iSum+len(markSummary) : iDiff])

	tail := text[iDiff+len(markDiff):]
	if iEnd := strings.Index(tail, markEnd); iEnd >= 0 {
		diff = tail[:iEnd]
	} else {
		diff = tail
	}
	diff = strings.TrimSpace(diff)
	diff = stripFences(diff)

	if !looksLikeUnifiedDiff(diff) {
		return "", "", fmt.Errorf("%w: %s", ErrMalformedDiff, excerpt(diff))
	}
	return summary, diff, nil
}

// stripFences removes a single leading/trailing triple-backtick fence
// if the model wrapped the diff anyway.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func looksLikeUnifiedDiff(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "--- a/") || strings.HasPrefix(t, "diff --git ")
}

func excerpt(s string) string {
	const max = 256
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func renderSuggestPrompt(in SuggestInput) string {
	goal := strings.TrimSpace(in.Goal)
	if goal == "" {
		goal = "improve readability, correctness, and conventions"
	}
	path := in.File.DisplayPath
	if path == "" {
		path = "file"
	}
	var b strings.Builder
	b.WriteString("You are reviewing a single dotfile and proposing improvements.\n")
	fmt.Fprintf(&b, "The file lives at: %s\n", path)
	fmt.Fprintf(&b, "Goal: %s\n\n", goal)
	b.WriteString("Respond with EXACTLY two sections in this order, each delimited by the marker lines shown.\n\n")
	b.WriteString("---SUMMARY---\n")
	b.WriteString("<one short sentence describing the change>\n")
	b.WriteString("---DIFF---\n")
	fmt.Fprintf(&b, "<unified diff with `a/%s` and `b/%s` headers; no surrounding fences and no ```markdown code fences>\n", path, path)
	b.WriteString("---END---\n\n")
	b.WriteString("Diff format rules (STRICT — `dfm apply` will reject anything else):\n")
	b.WriteString("- Do NOT wrap the diff in markdown code fences (no ``` before or after).\n")
	b.WriteString("- Every hunk header MUST have the form `@@ -<old-start>,<old-count> +<new-start>,<new-count> @@`.\n")
	b.WriteString("- A bare `@@` line with no ranges is INVALID and will be rejected. Always emit the full ranges, even for one-line edits.\n")
	b.WriteString("- `<old-count>` is the number of context + removed lines in the hunk (lines starting with ` ` or `-`).\n")
	b.WriteString("- `<new-count>` is the number of context + added lines in the hunk (lines starting with ` ` or `+`).\n")
	b.WriteString("- `<old-start>` / `<new-start>` are 1-based line numbers in the original / new file.\n\n")
	b.WriteString("Worked example — replacing line 11 inside a 3-line context window:\n")
	b.WriteString("@@ -10,3 +10,3 @@\n")
	b.WriteString(" unchanged line 10\n")
	b.WriteString("-old line 11\n")
	b.WriteString("+new line 11\n")
	b.WriteString(" unchanged line 12\n")
	b.WriteString("Here old-count=3 (one ` ` + one `-` + one ` `) and new-count=3 (one ` ` + one `+` + one ` `).\n\n")
	b.WriteString("Current contents:\n")
	b.WriteString("```\n")
	b.Write(in.Content)
	if len(in.Content) > 0 && in.Content[len(in.Content)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}

// execRun is the production Runner. It executes the binary with a
// scrubbed environment and returns its stdout.
func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = scrubEnv()
	return cmd.Output()
}

func scrubEnv() []string {
	keep := []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR"}
	var out []string
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CLAUDE_") || strings.HasPrefix(e, "ANTHROPIC_") {
			out = append(out, e)
		}
	}
	return out
}
