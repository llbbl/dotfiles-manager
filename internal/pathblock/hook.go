package pathblock

import (
	"fmt"
	"regexp"
)

// Fragment filenames are shared by the hook and fragment commands; zsh uses the POSIX file.
const (
	FragmentPosixName = "env.sh"
	FragmentFishName  = "env.fish"
)

// Hook markers are fixed because each file has one hook with no content to hash.
const (
	HookOpenMarker  = "# >>> dfm:env >>>"
	HookCloseMarker = "# <<< dfm:env <<<"
)

// hookBlockRe includes the installer's leading newline so removal restores the original bytes.
var hookBlockRe = regexp.MustCompile(
	`(?ms)\n?^[ \t]*` + regexp.QuoteMeta(HookOpenMarker) + `\n` +
		`.*?^[ \t]*` + regexp.QuoteMeta(HookCloseMarker) + `\n?`)

// hookPosixBody resolves the fragment at shell startup so rc files can sync across machines.
var hookPosixBody = fmt.Sprintf(`__dfm_env="${XDG_CONFIG_HOME:-$HOME/.config}/dotfiles/%s"
[ -r "$__dfm_env" ] && . "$__dfm_env"
unset __dfm_env
`, FragmentPosixName)

// hookFishBody spells out the XDG fallback because fish has no ${VAR:-default} expansion.
var hookFishBody = fmt.Sprintf(`set -l __dfm_base $XDG_CONFIG_HOME
test -n "$__dfm_base"; or set __dfm_base $HOME/.config
test -r $__dfm_base/dotfiles/%s; and source $__dfm_base/dotfiles/%s
set -e __dfm_base
`, FragmentFishName, FragmentFishName)

// RenderHook includes the trailing newline after the closing marker.
func RenderHook(family string) string {
	body := hookPosixBody
	if family == "fish" {
		body = hookFishBody
	}
	return HookOpenMarker + "\n" + body + HookCloseMarker + "\n"
}

// InstallHook replaces an existing block in place so updated bodies reach installed hooks.
func InstallHook(content []byte, family string) []byte {
	block := []byte(RenderHook(family))
	if loc := hookBlockRe.FindIndex(content); loc != nil {
		out := make([]byte, 0, len(content)+len(block))
		out = append(out, content[:loc[0]]...)
		if content[loc[0]] == '\n' {
			out = append(out, '\n')
		}
		out = append(out, block...)
		return append(out, content[loc[1]:]...)
	}
	out := make([]byte, 0, len(content)+len(block)+1)
	out = append(out, content...)
	if len(content) > 0 {
		out = append(out, '\n')
	}
	return append(out, block...)
}

// RemoveHook strips the hook and the separator newline written by InstallHook.
func RemoveHook(content []byte) []byte {
	return hookBlockRe.ReplaceAll(content, nil)
}

// KnownHookShell accepts the shells supported by hook file discovery.
func KnownHookShell(shell string) bool {
	switch shell {
	case "bash", "zsh", "fish", "sh", "profile":
		return true
	}
	return false
}
