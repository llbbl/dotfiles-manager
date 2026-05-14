// Package envfile parses a narrow subset of dotenv (".env") syntax and
// applies KEY=VALUE pairs to the process environment without clobbering
// keys already set by the shell.
//
// Supported grammar:
//
//   - KEY=VALUE
//   - KEY="quoted value"        (\n \t \\ \" escapes processed)
//   - KEY='single quoted'       (literal, no escapes)
//   - Leading "export " is accepted and stripped.
//   - Lines starting with # (after optional whitespace) are comments.
//   - Blank lines are skipped.
//
// Intentionally NOT supported:
//
//   - Variable interpolation (${VAR}, $VAR).
//   - Inline comments after unquoted values.
//
// Errors name the 1-based line number so users can locate the offending
// line quickly.
package envfile

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Parse decodes r into a KEY → VALUE map. Order is not preserved.
// Returns an error naming the line number on the first malformed line.
func Parse(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	scanner := bufio.NewScanner(r)
	// Allow large lines (default 64KB is fine for env values, but bump
	// the buffer so a long secret doesn't truncate silently).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Strip optional "export " prefix.
		if strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "export\t") {
			trimmed = strings.TrimLeft(trimmed[len("export"):], " \t")
		}

		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: missing '=' separator", lineNo)
		}
		key := strings.TrimRight(trimmed[:eq], " \t")
		val := trimmed[eq+1:]

		if err := validateKey(key); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}

		parsed, err := parseValue(val)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return out, nil
}

func validateKey(k string) error {
	if k == "" {
		return fmt.Errorf("empty key")
	}
	for i, r := range k {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return fmt.Errorf("key %q starts with a digit", k)
			}
		default:
			return fmt.Errorf("invalid character %q in key %q", r, k)
		}
	}
	return nil
}

func parseValue(raw string) (string, error) {
	// Leading whitespace between '=' and value is stripped for ergonomics.
	v := strings.TrimLeft(raw, " \t")
	if v == "" {
		return "", nil
	}
	switch v[0] {
	case '"':
		return parseDoubleQuoted(v)
	case '\'':
		return parseSingleQuoted(v)
	default:
		// Unquoted: strip trailing whitespace. No inline-comment support.
		return strings.TrimRight(v, " \t"), nil
	}
}

func parseDoubleQuoted(v string) (string, error) {
	// v starts with '"'. Walk until the closing unescaped quote.
	var sb strings.Builder
	for i := 1; i < len(v); i++ {
		c := v[i]
		if c == '\\' {
			if i+1 >= len(v) {
				return "", fmt.Errorf("dangling escape in double-quoted value")
			}
			next := v[i+1]
			switch next {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case '\\':
				sb.WriteByte('\\')
			case '"':
				sb.WriteByte('"')
			default:
				return "", fmt.Errorf("unsupported escape \\%c in double-quoted value", next)
			}
			i++
			continue
		}
		if c == '"' {
			// Allow trailing whitespace after the closing quote.
			rest := strings.TrimLeft(v[i+1:], " \t")
			if rest != "" {
				return "", fmt.Errorf("unexpected trailing content after double-quoted value")
			}
			return sb.String(), nil
		}
		sb.WriteByte(c)
	}
	return "", fmt.Errorf("unterminated double-quoted value")
}

func parseSingleQuoted(v string) (string, error) {
	// v starts with '\''. Single quotes are literal — no escapes.
	end := strings.IndexByte(v[1:], '\'')
	if end < 0 {
		return "", fmt.Errorf("unterminated single-quoted value")
	}
	rest := strings.TrimLeft(v[1+end+1:], " \t")
	if rest != "" {
		return "", fmt.Errorf("unexpected trailing content after single-quoted value")
	}
	return v[1 : 1+end], nil
}

// Load parses the file at path and applies each KEY=VALUE pair via
// os.Setenv ONLY when os.Getenv(KEY) is currently empty. Returns the
// number of keys actually set in the process env, the keys skipped
// because they were pre-set in the env, and any parse/IO error.
//
// File permission auditing is the caller's job; Load is purely about
// parsing + applying.
func Load(path string) (set int, skipped []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()

	m, err := Parse(f)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range m {
		if os.Getenv(k) != "" {
			skipped = append(skipped, k)
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return set, skipped, fmt.Errorf("setenv %s: %w", k, err)
		}
		set++
	}
	return set, skipped, nil
}
