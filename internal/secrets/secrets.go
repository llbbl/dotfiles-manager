// Package secrets implements a pre-flight scanner that detects obvious
// credentials in plain-text files, used by the `track` command to refuse
// management of files that look like they contain secrets.
package secrets

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// MaxBytes is the upper limit on file size the scanner will read.
const MaxBytes = 1 << 20

const (
	maxFindings   = 100
	binarySniffSz = 8 * 1024
)

// Finding represents one rule hit on one line.
type Finding struct {
	Rule    string `json:"rule"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
}

// Result is the outcome of scanning a single source.
type Result struct {
	Findings []Finding `json:"findings"`
	Skipped  bool      `json:"skipped"`
	Reason   string    `json:"reason,omitempty"`
}

type rule struct {
	name        string
	re          *regexp.Regexp
	description string
	// needsContext, when set, forces a match to also include the given
	// substring (case-insensitive) on either the current or previous line.
	needsContext string
}

// rules is the single source of truth for the detection table.
// Adding a rule is a single struct-literal addition.
var rules = []rule{
	{
		name:        "aws-access-key",
		re:          regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		description: "AWS access key id",
	},
	{
		name:         "aws-secret-key",
		re:           regexp.MustCompile(`\b[A-Za-z0-9/+=]{40}\b`),
		description:  "AWS secret access key (paired with context word)",
		needsContext: "aws_secret_access_key",
	},
	{
		name:        "private-key",
		re:          regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |PGP |DSA |ENCRYPTED )?PRIVATE KEY-----`),
		description: "PEM private key block",
	},
	{
		name:        "github-token",
		re:          regexp.MustCompile(`\bgh[poursr]_[A-Za-z0-9]{36}\b`),
		description: "GitHub personal/OAuth/user/server/refresh token",
	},
	{
		name:        "slack-token",
		re:          regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
		description: "Slack token",
	},
	{
		name:        "generic-jwt",
		re:          regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_=-]+\b`),
		description: "Generic JWT",
	},
	{
		name:        "anthropic-key",
		re:          regexp.MustCompile(`\bsk-ant-[A-Za-z0-9-]{20,}\b`),
		description: "Anthropic API key",
	},
	{
		name:        "openai-key",
		re:          regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
		description: "OpenAI API key",
	},
	{
		name:        "env-like",
		re:          regexp.MustCompile(`(?i)^(.*_)?(SECRET|TOKEN|API[_-]?KEY|PASSWORD|PASSWD|PRIVATE[_-]?KEY|ACCESS[_-]?KEY)=(.+)$`),
		description: ".env-style assignment with non-empty value",
	},
}

// ScanFile reads the file at path and runs all rules. It always closes the file.
func ScanFile(path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return ScanReader(f)
}

// ScanReader runs all rules against an io.Reader.
func ScanReader(r io.Reader) (Result, error) {
	// Buffer up to MaxBytes+1 so we can detect oversize input.
	limited := io.LimitReader(r, MaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, fmt.Errorf("read: %w", err)
	}
	if len(data) > MaxBytes {
		return Result{Skipped: true, Reason: "file exceeds 1 MiB"}, nil
	}

	sniff := data
	if len(sniff) > binarySniffSz {
		sniff = sniff[:binarySniffSz]
	}
	for _, b := range sniff {
		if b == 0x00 {
			return Result{Skipped: true, Reason: "binary file"}, nil
		}
	}

	return scanBytes(data), nil
}

func scanBytes(data []byte) Result {
	res := Result{Findings: []Finding{}}
	br := bufio.NewReader(strings.NewReader(string(data)))

	var prev string
	lineNo := 0
	truncated := false

	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			lineNo++
			trimmed := strings.TrimRight(line, "\r\n")
			for _, rl := range rules {
				if truncated {
					break
				}
				loc := rl.re.FindStringIndex(trimmed)
				if loc == nil {
					continue
				}
				if rl.needsContext != "" {
					ctx := strings.ToLower(trimmed) + "\n" + strings.ToLower(prev)
					if !strings.Contains(ctx, rl.needsContext) {
						continue
					}
				}
				if rl.name == "env-like" {
					m := rl.re.FindStringSubmatch(trimmed)
					if len(m) >= 4 {
						rhs := strings.TrimSpace(m[3])
						if rhs == "" || rhs == `""` || rhs == `''` || len(rhs) < 8 {
							continue
						}
					}
				}
				if rl.name == "openai-key" && strings.HasPrefix(trimmed[loc[0]:], "sk-ant-") {
					// avoid double-flagging anthropic keys under openai-key
					continue
				}
				match := trimmed[loc[0]:loc[1]]
				res.Findings = append(res.Findings, Finding{
					Rule:    rl.name,
					Line:    lineNo,
					Excerpt: buildExcerpt(trimmed, loc[0], loc[1], match),
				})
				if len(res.Findings) >= maxFindings {
					truncated = true
					break
				}
			}
			prev = trimmed
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// Should not happen with strings.NewReader, but stay safe.
				return res
			}
			break
		}
	}

	if truncated {
		res.Findings = append(res.Findings, Finding{
			Rule:    "truncated",
			Line:    -1,
			Excerpt: "result truncated at 100 findings",
		})
	}
	return res
}

// Mask reduces a secret value to a short, non-reversible preview.
func Mask(s string) string {
	if len(s) >= 8 {
		return s[:4] + "…" + s[len(s)-2:]
	}
	return "•••"
}

// buildExcerpt masks the matched substring in the line and trims to 80 chars,
// trying to keep the masked match roughly centered.
func buildExcerpt(line string, start, end int, match string) string {
	masked := Mask(match)
	out := line[:start] + masked + line[end:]
	const width = 80
	if len(out) <= width {
		return out
	}
	maskStart := start
	maskEnd := start + len(masked)
	center := (maskStart + maskEnd) / 2
	half := width / 2
	begin := center - half
	if begin < 0 {
		begin = 0
	}
	finish := begin + width
	if finish > len(out) {
		finish = len(out)
		begin = finish - width
		if begin < 0 {
			begin = 0
		}
	}
	snippet := out[begin:finish]
	if begin > 0 {
		snippet = "…" + snippet[1:]
	}
	if finish < len(out) {
		snippet = snippet[:len(snippet)-1] + "…"
	}
	return snippet
}
