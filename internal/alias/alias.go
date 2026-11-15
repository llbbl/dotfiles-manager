// Package alias renders, finds and rewrites the dfm-managed alias blocks
// in shell rc file content.
package alias

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidName accepts the alias names portable across bash, zsh, fish and
// POSIX sh. Hyphens are rejected: legal in some shells but not portable.
var ValidName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidGroup accepts `--group` names. It admits no regex metacharacters,
// so a validated group is interpolated into fence regexes without QuoteMeta.
var ValidGroup = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// Line renders one alias definition with its trailing newline. Both families
// single-quote the body: posix closes and reopens the quote around each
// literal quote, fish backslash-escapes backslashes and quotes.
func Line(family, name, command string) string {
	if family == "fish" {
		escaped := strings.ReplaceAll(command, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `'`, `\'`)
		return fmt.Sprintf("alias %s '%s'\n", name, escaped)
	}
	// posix: replace ' with '\''
	escaped := strings.ReplaceAll(command, `'`, `'\''`)
	return fmt.Sprintf("alias %s='%s'\n", name, escaped)
}

// MatchRe matches any line defining name in the given family, whether it
// sits inside a managed block or bare outside one.
func MatchRe(family, name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	if family == "fish" {
		return regexp.MustCompile(`^\s*alias\s+` + q + `\s+`)
	}
	return regexp.MustCompile(`^\s*alias\s+` + q + `\s*=`)
}

// LegacyBlockRe matches the per-alias fenced block older dfm versions wrote.
// It is still recognised for dup-detect, remove and --replace, never written.
func LegacyBlockRe(name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	return regexp.MustCompile(
		`(?m)^[ \t]*# >>> dfm:alias ` + q + ` >>>\n` +
			`[^\n]*\n` +
			`[ \t]*# <<< dfm:alias ` + q + ` <<<\n?`)
}

// DefaultBlockRe matches the shared `dfm:aliases` block; submatch 1 is the
// body. The trailing newline is optional so a block at EOF still matches.
func DefaultBlockRe() *regexp.Regexp {
	return regexp.MustCompile(
		`(?ms)^[ \t]*# >>> dfm:aliases >>>\n` +
			`(.*?)` +
			`^[ \t]*# <<< dfm:aliases <<<\n?`)
}

// GroupBlockRe matches the named group's block; submatch 1 is the body.
// group must already pass ValidGroup, since it is interpolated unquoted.
func GroupBlockRe(group string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?ms)^[ \t]*# >>> dfm:group ` + group + ` >>>\n` +
			`(.*?)` +
			`^[ \t]*# <<< dfm:group ` + group + ` <<<\n?`)
}

// AnyGroupBlockRe matches any group block: submatch 1 is the open name, 2 the
// body, 3 the close name. RE2 has no backreferences, so callers check 1 == 3.
func AnyGroupBlockRe() *regexp.Regexp {
	return regexp.MustCompile(
		`(?ms)^[ \t]*# >>> dfm:group ([A-Za-z][A-Za-z0-9_-]*) >>>\n` +
			`(.*?)` +
			`^[ \t]*# <<< dfm:group ([A-Za-z][A-Za-z0-9_-]*) <<<\n?`)
}

// RenderDefaultBlock builds the shared default block from newline-terminated
// body lines.
func RenderDefaultBlock(bodyLines []string) string {
	var b strings.Builder
	b.WriteString("# >>> dfm:aliases >>>\n")
	for _, ln := range bodyLines {
		b.WriteString(ln)
	}
	b.WriteString("# <<< dfm:aliases <<<\n")
	return b.String()
}

// RenderGroupBlock builds a named group block. group must already pass
// ValidGroup.
func RenderGroupBlock(group string, bodyLines []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# >>> dfm:group %s >>>\n", group)
	for _, ln := range bodyLines {
		b.WriteString(ln)
	}
	fmt.Fprintf(&b, "# <<< dfm:group %s <<<\n", group)
	return b.String()
}

// BlockBodyLines splits a captured block body into newline-terminated lines.
func BlockBodyLines(body string) []string {
	if body == "" {
		return nil
	}
	parts := strings.SplitAfter(body, "\n")
	// SplitAfter keeps the separator; trim a trailing empty element if
	// body didn't end on a newline (it always should, but be defensive).
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// Count returns how many times name is defined in content: legacy blocks,
// shared and group block body lines, and bare lines outside any block.
func Count(family, name, content string) int {
	matchRe := MatchRe(family, name)
	legacyRe := LegacyBlockRe(name)
	defaultRe := DefaultBlockRe()
	groupRe := AnyGroupBlockRe()

	legacyBlocks := len(legacyRe.FindAllStringIndex(content, -1))

	// Count body-line hits inside each managed block.
	sharedHits := 0
	if m := defaultRe.FindStringSubmatch(content); m != nil {
		body := m[1]
		for _, ln := range BlockBodyLines(body) {
			if matchRe.MatchString(ln) {
				sharedHits++
			}
		}
	}
	for _, sub := range groupRe.FindAllStringSubmatch(content, -1) {
		// Skip malformed blocks where the open and close names don't
		// agree (Go's RE2 can't pin them in the regex; we check here).
		if sub[1] != sub[3] {
			continue
		}
		body := sub[2]
		for _, ln := range BlockBodyLines(body) {
			if matchRe.MatchString(ln) {
				sharedHits++
			}
		}
	}

	// Bare-line scan over the whole file. We then subtract the lines
	// that we already counted inside legacy blocks (1 body line each)
	// and shared blocks (sharedHits, already counted explicitly).
	bareTotal := 0
	for ln := range strings.SplitSeq(content, "\n") {
		if matchRe.MatchString(ln) {
			bareTotal++
		}
	}
	bareOutside := max(bareTotal-legacyBlocks-sharedHits, 0)

	return legacyBlocks + sharedHits + bareOutside
}

// RemovalCounts breaks down what RemoveEverywhere removed, for the audit log.
type RemovalCounts struct {
	LegacyBlocks  int // number of legacy per-alias blocks removed
	SharedEvicted int // body lines pulled out of shared blocks
	EmptiedBlocks int // shared blocks that ended up empty and were dropped
	BareLines     int // stray un-fenced bare lines removed
}

// RemoveEverywhere strips every definition of name from content, in every
// storage form, and drops any shared block it leaves empty.
func RemoveEverywhere(family, name, content string) (string, RemovalCounts) {
	var rc RemovalCounts
	matchRe := MatchRe(family, name)

	// Phase 1: drop every legacy per-alias block for this name.
	legacyRe := LegacyBlockRe(name)
	rc.LegacyBlocks = len(legacyRe.FindAllStringIndex(content, -1))
	content = legacyRe.ReplaceAllString(content, "")

	// Phase 2a: rewrite the default shared block (if present), pruning
	// any body lines that define this alias. If the block ends up empty,
	// remove it entirely.
	defaultRe := DefaultBlockRe()
	content = defaultRe.ReplaceAllStringFunc(content, func(full string) string {
		m := defaultRe.FindStringSubmatch(full)
		body := m[1]
		kept := make([]string, 0)
		for _, ln := range BlockBodyLines(body) {
			if matchRe.MatchString(ln) {
				rc.SharedEvicted++
				continue
			}
			kept = append(kept, ln)
		}
		if len(kept) == 0 {
			rc.EmptiedBlocks++
			return ""
		}
		return RenderDefaultBlock(kept)
	})

	// Phase 2b: same treatment for every named-group block.
	groupRe := AnyGroupBlockRe()
	content = groupRe.ReplaceAllStringFunc(content, func(full string) string {
		m := groupRe.FindStringSubmatch(full)
		// Mismatched open/close fence names: leave the text alone.
		if m[1] != m[3] {
			return full
		}
		groupName := m[1]
		body := m[2]
		kept := make([]string, 0)
		for _, ln := range BlockBodyLines(body) {
			if matchRe.MatchString(ln) {
				rc.SharedEvicted++
				continue
			}
			kept = append(kept, ln)
		}
		if len(kept) == 0 {
			rc.EmptiedBlocks++
			return ""
		}
		return RenderGroupBlock(groupName, kept)
	})

	// Phase 3: sweep any remaining bare lines (legacy un-fenced entries
	// from before dfm fenced anything).
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if matchRe.MatchString(ln) {
			rc.BareLines++
			continue
		}
		kept = append(kept, ln)
	}
	content = strings.Join(kept, "\n")

	return content, rc
}

// Upsert appends bodyLine to the target block, creating the block at EOF if
// absent; group "" means the default block. group must already pass
// ValidGroup because GroupBlockRe compiles it unescaped. The second result
// is "appended" or "created".
func Upsert(content, group, bodyLine string) (string, string) {
	var re *regexp.Regexp
	if group == "" {
		re = DefaultBlockRe()
	} else {
		re = GroupBlockRe(group)
	}
	if loc := re.FindStringSubmatchIndex(content); loc != nil {
		// loc[2]/loc[3] is the body capture range.
		bodyStart, bodyEnd := loc[2], loc[3]
		body := content[bodyStart:bodyEnd]
		lines := BlockBodyLines(body)
		lines = append(lines, bodyLine)
		var rebuilt string
		if group == "" {
			rebuilt = RenderDefaultBlock(lines)
		} else {
			rebuilt = RenderGroupBlock(group, lines)
		}
		return content[:loc[0]] + rebuilt + content[loc[1]:], "appended"
	}
	// No existing block: append a fresh one. Guarantee a newline
	// separator if the file doesn't already end in \n (or is empty,
	// in which case we want nothing before the block).
	prefix := content
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	var block string
	if group == "" {
		block = RenderDefaultBlock([]string{bodyLine})
	} else {
		block = RenderGroupBlock(group, []string{bodyLine})
	}
	return prefix + block, "created"
}

// The list regexes are best-effort: single-line `alias name='body'` forms
// only, with no escaped quotes or multi-line definitions.
var (
	listPosixRe = regexp.MustCompile(`^\s*alias\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(['"])(.+)(['"])\s*$`)
	listFishRe  = regexp.MustCompile(`^\s*alias\s+([A-Za-z_][A-Za-z0-9_]*)\s+(['"])(.+)(['"])\s*$`)
)

// Entry is one alias found by Parse. Command is the text between the quotes,
// not unescaped.
type Entry struct {
	Name    string
	Command string
}

// Parse returns the aliases it recognises in content, skipping any line
// the family's list regex does not match.
func Parse(family, content string) []Entry {
	re := listPosixRe
	if family == "fish" {
		re = listFishRe
	}
	var out []Entry
	for ln := range strings.SplitSeq(content, "\n") {
		m := re.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		// m[1]=name, m[3]=body. We deliberately don't un-escape.
		out = append(out, Entry{Name: m[1], Command: m[3]})
	}
	return out
}
