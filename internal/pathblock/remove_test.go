package pathblock

import (
	"strings"
	"testing"
)

// A CRLF rc file gets the same byte-identical promise as an LF one: the
// blank separator an add wrote is "\r\n" there, not "\n".
func TestRemovePathBlocks_CRLFSeparator(t *testing.T) {
	for _, tc := range []struct{ name, eol string }{
		{"lf", "\n"},
		{"crlf", "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := "# mine" + tc.eol + "export EDITOR=vim" + tc.eol
			block := Render("posix", MarkerID(DirectionPrepend, []string{"/a"}),
				goldenTime, DirectionPrepend, []string{"/a"})
			if tc.eol == "\r\n" {
				block = strings.ReplaceAll(block, "\n", "\r\n")
			}
			content := []byte(head + tc.eol + block)

			got := RemoveBlocks(content, FindEntries(content))
			if string(got) != head {
				t.Errorf("after remove = %q, want %q", got, head)
			}
		})
	}
}
